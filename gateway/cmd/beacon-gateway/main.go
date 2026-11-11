// Command beacon-gateway attaches Beacon to LLM gateways: thin adapters connect it to LiteLLM,
// Envoy, Portkey, or any gateway that can call out, and one shared pipeline records what each
// model call carried as Beacon events.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

const version = "0.2.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "beacon-gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "configuration file (default: built-in LiteLLM observe-only config)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	env := Env{Log: log, Version: version}
	analyzers, err := cfg.analyzers(env)
	if err != nil {
		return err
	}
	policy, err := cfg.policy(env)
	if err != nil {
		return err
	}
	observers, err := cfg.observers(env)
	if err != nil {
		return err
	}
	adapters, err := cfg.gateways(log)
	if err != nil {
		return err
	}
	routes, err := cfg.routes(env)
	if err != nil {
		return err
	}
	st, err := cfg.store(env)
	if err != nil {
		return err
	}
	cfg.Debug.apply()
	memLimit, err := cfg.GC.apply(log)
	if err != nil {
		return err
	}
	var bodyBytes int64
	switch {
	case cfg.Pipeline.MaxBodyBytes != "":
		if bodyBytes, err = parseBytes(cfg.Pipeline.MaxBodyBytes); err != nil {
			return fmt.Errorf("pipeline.max_body_bytes: %w", err)
		}
	case memLimit > 0:
		bodyBytes = memLimit / 4
	}
	p := pipeline.New(pipeline.Config{
		Analyzers:      analyzers,
		Policy:         policy,
		Observers:      observers,
		Routes:         routes,
		Store:          st,
		SeenTTL:        cfg.Pipeline.SeenTTL,
		SessionHeaders: cfg.Pipeline.SessionHeaders,
		MaxBodyBytes:   bodyBytes,
		MaxInFlight:    cfg.Pipeline.MaxInFlight,
		QueueWait:      cfg.Pipeline.QueueWait,
		Log:            log,
	})
	defer p.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	names := make([]string, len(analyzers))
	for i, a := range analyzers {
		names[i] = a.Name()
	}
	log.Info("starting", "version", version, "gateways", len(adapters), "analyzers", names, "answers_first", p.AnswersFirst(), "content", cfg.Content.Retention)
	return serve(ctx, stop, p, adapters, cfg.Admin, cfg.Debug.Pprof, log)
}

// serve runs every adapter and the admin endpoint until ctx ends or one of them fails, and
// returns once all have stopped, so the pipeline is closed only after the last call.
func serve(ctx context.Context, stop context.CancelFunc, p *pipeline.Pipeline, adapters []gateway.Adapter, admin string, pprof bool, log *slog.Logger) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	fail := func(name string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if first == nil {
			first = fmt.Errorf("%s: %w", name, err)
		}
		mu.Unlock()
		stop()
	}
	for _, a := range adapters {
		wg.Go(func() { fail(a.Name(), a.Serve(ctx, p)) })
	}
	if admin != "off" {
		wg.Go(func() {
			fail("admin", gateway.ServeHTTP(ctx, admin, adminHandler(p, pprof), log.With("server", "admin")))
		})
	}
	wg.Wait()
	if errors.Is(first, context.Canceled) {
		return nil
	}
	return first
}

// adminHandler serves health, counters (the pipeline's and the Go runtime's) and, when enabled,
// pprof. The admin listener binds localhost by default; pprof exposes internals, so it is off
// unless configured.
func adminHandler(p *pipeline.Pipeline, pprof bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			pipeline.Stats
			Runtime runtimeStats `json:"runtime"`
		}{p.Stats(), runtimeStats{
			Goroutines:   runtime.NumGoroutine(),
			HeapAlloc:    m.HeapAlloc,
			HeapInuse:    m.HeapInuse,
			Sys:          m.Sys,
			NumGC:        m.NumGC,
			PauseTotalMS: float64(m.PauseTotalNs) / 1e6,
			GCCPU:        m.GCCPUFraction,
		}})
	})
	if pprof {
		mux.HandleFunc("GET /debug/pprof/", httppprof.Index)
		mux.HandleFunc("GET /debug/pprof/cmdline", httppprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", httppprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", httppprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", httppprof.Trace)
	}
	return mux
}

type runtimeStats struct {
	Goroutines   int     `json:"goroutines"`
	HeapAlloc    uint64  `json:"heap_alloc"`
	HeapInuse    uint64  `json:"heap_inuse"`
	Sys          uint64  `json:"sys"`
	NumGC        uint32  `json:"num_gc"`
	PauseTotalMS float64 `json:"gc_pause_total_ms"`
	GCCPU        float64 `json:"gc_cpu_fraction"`
}
