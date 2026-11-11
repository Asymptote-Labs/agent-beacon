// Command stress is a network load generator for a running beacon-gateway: it plays synthetic
// Claude Code sessions (internal/synth) against the real adapters (LiteLLM guardrail callouts,
// Portkey webhooks, Envoy ext_proc streams) at up to 100k concurrent in-flight calls, samples the
// server's /stats every second, optionally pulls pprof profiles, and reports latency, errors and
// verdict correctness. The bad call of each bad session is expected to be allowed unless
// -expect-blocks says the server runs a policy that blocks it.
//
// Each worker is one in-flight slot. It advances a session turn by turn: the request-phase call,
// then (unless blocked) the response-phase call. Bodies are rendered once per script variant and
// sent as shared pieces; only call and session ids are written per call.
//
//	go build -o /tmp/bgw-server ./cmd/beacon-gateway && mkdir -p /tmp/bgw-stress
//	/tmp/bgw-server -config scripts/stress/beacon-gateway.yaml &
//	go run ./scripts/stress -scenario steady -target mixed -concurrency 200 -duration 10s
//
// Scenarios: steady, burst, soak, hotsession, sessionstorm, bigbody, envoy-streamed, chaos.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
	"github.com/asymptote-labs/agent-beacon/gateway/litellm"
)

type config struct {
	target, proto, scenario    string
	conns, concurrency         int
	ramp, duration, timeout    time.Duration
	sessions, turnMax          int
	badRatio                   float64
	expectBlocks               bool
	admin, profileDir          string
	seed                       uint64
	cpus, variants, resultSize int
	litellm, portkey, envoy    string
	portkeyPath                string
	runID                      string
	abortHeapMiB               int
}

var scenarios = []string{"steady", "burst", "soak", "hotsession", "sessionstorm", "bigbody", "envoy-streamed", "chaos"}

func main() {
	var cfg config
	flag.StringVar(&cfg.target, "target", "mixed", "litellm | portkey | envoy | mixed (workers 50/20/30)")
	flag.StringVar(&cfg.proto, "proto", "h1", "HTTP targets: h1 | h2c (prior knowledge)")
	flag.IntVar(&cfg.conns, "conns", 0, "connections per listener port (HTTP pool size; Envoy: gRPC connections); 0 = auto")
	flag.IntVar(&cfg.concurrency, "concurrency", 200, "target in-flight calls (one worker each)")
	flag.DurationVar(&cfg.ramp, "ramp", 5*time.Second, "time to start every worker")
	flag.DurationVar(&cfg.duration, "duration", 30*time.Second, "steady-state time (burst: time spent bursting)")
	flag.IntVar(&cfg.sessions, "sessions", 0, "distinct concurrent sessions (0 = -concurrency); fewer than workers share ids")
	flag.IntVar(&cfg.turnMax, "turn-max", 100, "session length in turns; bounds body size")
	flag.Float64Var(&cfg.badRatio, "bad-ratio", 0.02, "fraction of sessions ending in a bad call (curl|sh reply or injected tool result)")
	flag.BoolVar(&cfg.expectBlocks, "expect-blocks", false, "expect each bad call to be blocked (the server runs a policy that blocks it); otherwise every call is expected to be allowed")
	flag.StringVar(&cfg.scenario, "scenario", "steady", strings.Join(scenarios, " | "))
	flag.StringVar(&cfg.admin, "admin", "127.0.0.1:4029", "server admin address (/stats, /debug/pprof)")
	flag.StringVar(&cfg.profileDir, "profile-dir", "", "save server CPU profile, trace, heap, allocs, goroutine, mutex and block profiles here")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Second, "per call (Envoy: per exchange)")
	flag.Uint64Var(&cfg.seed, "seed", 1, "seed for sessions and worker choices")
	flag.IntVar(&cfg.cpus, "cpus", 0, "client GOMAXPROCS (0 = all)")
	flag.IntVar(&cfg.variants, "variants", 8, "distinct session scripts")
	flag.IntVar(&cfg.resultSize, "result-bytes", 3000, "bytes per tool result")
	flag.StringVar(&cfg.litellm, "litellm", "127.0.0.1:4100-4107", "LiteLLM guardrail listeners (host:port, host:lo-hi, comma-separated)")
	flag.StringVar(&cfg.portkey, "portkey", "127.0.0.1:4110-4113", "Portkey webhook listeners")
	flag.StringVar(&cfg.portkeyPath, "portkey-path", "/portkey/webhook", "Portkey webhook path")
	flag.StringVar(&cfg.envoy, "envoy", "127.0.0.1:4120-4121", "Envoy ext_proc listeners")
	flag.StringVar(&cfg.runID, "run-id", "", "prefix of every session id (default: random per run)")
	flag.IntVar(&cfg.abortHeapMiB, "abort-heap-mib", 0, "stop the run when the server's runtime.heap_inuse exceeds this (MiB); 0 = never")
	flag.Parse()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "stress:", err)
		os.Exit(1)
	}
}

type runner struct {
	cfg     config
	m       *metrics
	scripts []*synth.Script
	runID   string
	hotID   []byte
	stats   *sampler
	workers []*worker
	abort   context.CancelFunc
	aborted atomic.Pointer[string]
	ports   [nTargets][]string
	conns   [nTargets]int
	notes   []string

	start      time.Time
	phase      atomic.Pointer[string]
	steadyFrom time.Time
	steadyTo   time.Time
	inflightS  []float64 // in-flight sampled each second of steady state
	maxInfl    int64
	cpuStart   rusage
	bursts     []string
}

func (r *runner) setPhase(s string) { r.phase.Store(&s) }

func run(cfg config) error {
	if err := adjust(&cfg); err != nil {
		return err
	}
	if cfg.cpus > 0 {
		runtime.GOMAXPROCS(cfg.cpus)
	}
	r := &runner{cfg: cfg, runID: cfg.runID}
	if r.runID == "" {
		r.runID = fmt.Sprintf("st%08x", rand.Uint32())
	}
	r.hotID = []byte(r.runID + "-hot")
	r.m = newMetrics(4 * runtime.GOMAXPROCS(0))
	r.stats = &sampler{url: "http://" + cfg.admin + "/stats"}
	r.notes = cfg.notes()
	r.setPhase("setup")

	t0 := time.Now()
	r.scripts = make([]*synth.Script, cfg.variants)
	var wg sync.WaitGroup
	for v := range r.scripts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.scripts[v] = synth.NewScript(cfg.seed, v, cfg.turnMax, cfg.resultSize)
		}()
	}
	wg.Wait()
	if cfg.scenario == "envoy-streamed" {
		for _, s := range r.scripts {
			for k := 1; k <= s.Turns(); k++ {
				s.SSE(k, synth.Benign, true)
			}
			s.SSE(1, synth.CurlPipe, true)
		}
	}
	if cfg.scenario == "chaos" {
		gzipBomb()
	}
	fmt.Printf("scripts: %d variants × %d turns rendered in %s; Anthropic request body turn 1 %s, turn %d %s\n",
		cfg.variants, cfg.turnMax, time.Since(t0).Round(time.Millisecond),
		bytesStr(float64(size(r.scripts[0].AnthropicRequest(nil, 1, []byte("u"), synth.Benign)))), cfg.turnMax,
		bytesStr(float64(size(r.scripts[0].AnthropicRequest(nil, cfg.turnMax, []byte("u"), synth.Benign)))))

	if err := r.setup(); err != nil {
		return err
	}
	if s := r.stats.fetch(context.Background()); s.err != nil {
		return fmt.Errorf("server admin %s unreachable: %w", cfg.admin, s.err)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, r.abort = context.WithCancel(ctx)
	defer r.abort()
	r.start = time.Now()
	r.cpuStart = getRusage()
	tl := r.timeline(ctx)
	if cfg.scenario == "burst" {
		r.runBurst(ctx)
	} else {
		r.runSteady(ctx)
	}
	tl()
	r.stats.fetch(context.Background())
	r.report()
	return nil
}

// adjust validates flags and applies each scenario's fixed settings.
func adjust(cfg *config) error {
	if cfg.concurrency < 1 || cfg.turnMax < 1 || cfg.variants < 1 || cfg.resultSize < 1 {
		return fmt.Errorf("-concurrency, -turn-max, -variants and -result-bytes must be positive")
	}
	if cfg.badRatio < 0 || cfg.badRatio > 1 {
		return fmt.Errorf("-bad-ratio must be in [0,1]")
	}
	switch cfg.target {
	case "litellm", "portkey", "envoy", "mixed":
	default:
		return fmt.Errorf("unknown -target %q", cfg.target)
	}
	if cfg.proto != "h1" && cfg.proto != "h2c" {
		return fmt.Errorf("unknown -proto %q", cfg.proto)
	}
	known := false
	for _, s := range scenarios {
		known = known || s == cfg.scenario
	}
	if !known {
		return fmt.Errorf("unknown -scenario %q (%s)", cfg.scenario, strings.Join(scenarios, ", "))
	}
	switch cfg.scenario {
	case "hotsession":
		cfg.sessions, cfg.badRatio = 1, 0
	case "bigbody":
		cfg.turnMax = 400
	case "envoy-streamed":
		cfg.target = "envoy"
	}
	if cfg.sessions <= 0 {
		cfg.sessions = cfg.concurrency
	}
	return nil
}

func (cfg config) notes() []string {
	switch cfg.scenario {
	case "hotsession":
		return []string{"hotsession: every worker plays its own turns under ONE session id; -sessions=1, -bad-ratio=0 forced"}
	case "bigbody":
		return []string{"bigbody: -turn-max=400 forced; sessions resume in the last quarter, so bodies are 1.1-1.5 MB"}
	case "envoy-streamed":
		return []string{"envoy-streamed: -target=envoy forced; replies are ~120 KB SSE in 1-64 byte chunks (STREAMED), bad replies are recorded, not enforced"}
	case "sessionstorm":
		return []string{"sessionstorm: every turn uses a brand-new session id"}
	}
	return nil
}

func expandAddrs(spec string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.LastIndex(part, ":")
		if i < 0 {
			return nil, fmt.Errorf("address %q has no port", part)
		}
		host, ports := part[:i], part[i+1:]
		lo, hi, isRange := strings.Cut(ports, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("address %q: %w", part, err)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("address %q: bad port range", part)
			}
		}
		for p := a; p <= b; p++ {
			out = append(out, host+":"+strconv.Itoa(p))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no addresses in %q", spec)
	}
	return out, nil
}

// targetOf assigns worker i its target: mixed is 50% LiteLLM, 20% Portkey, 30% Envoy.
func (cfg config) targetOf(i int) target {
	switch cfg.target {
	case "litellm":
		return tLiteLLM
	case "portkey":
		return tPortkey
	case "envoy":
		return tEnvoy
	}
	switch x := i % 10; {
	case x < 5:
		return tLiteLLM
	case x < 7:
		return tPortkey
	}
	return tEnvoy
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// setup builds the workers, their sessions and their routes to the server.
func (r *runner) setup() error {
	cfg := r.cfg
	var err error
	for t, spec := range [nTargets]string{cfg.litellm, cfg.portkey, cfg.envoy} {
		if r.ports[t], err = expandAddrs(spec); err != nil {
			return err
		}
	}
	var count [nTargets]int
	for i := range cfg.concurrency {
		count[cfg.targetOf(i)]++
	}
	for t := range nTargets {
		n, ports := count[t], len(r.ports[t])
		switch {
		case n == 0:
		case cfg.conns > 0:
			r.conns[t] = cfg.conns
		case t == tEnvoy:
			r.conns[t] = max(4, ceilDiv(n, ports*1000))
		case cfg.proto == "h2c":
			r.conns[t] = max(1, ceilDiv(n, ports*800))
		default:
			r.conns[t] = ceilDiv(n, ports)
		}
	}

	var rts [nTargets][][]*httpConn
	for _, t := range []target{tLiteLLM, tPortkey} {
		if count[t] == 0 {
			continue
		}
		path := litellm.Path
		if t == tPortkey {
			path = cfg.portkeyPath
		}
		for _, addr := range r.ports[t] {
			var conns []*httpConn
			for _, rt := range newTransports(cfg.proto, r.conns[t]) {
				conns = append(conns, &httpConn{rt: rt, url: &url.URL{Scheme: "http", Host: addr, Path: path}, host: addr, h2: cfg.proto == "h2c"})
			}
			rts[t] = append(rts[t], conns)
		}
	}
	var envoy [][]*envoyConn
	if count[tEnvoy] > 0 {
		for _, addr := range r.ports[tEnvoy] {
			var conns []*envoyConn
			for range r.conns[tEnvoy] {
				cc, err := dialEnvoy(addr)
				if err != nil {
					return err
				}
				conns = append(conns, &envoyConn{client: extprocv3.NewExternalProcessorClient(cc)})
			}
			envoy = append(envoy, conns)
		}
	}

	var widx [nTargets]int
	r.workers = make([]*worker, cfg.concurrency)
	for i := range r.workers {
		t := cfg.targetOf(i)
		w := &worker{
			id: i, widx: widx[t], tgt: t, run: r,
			r:  rand.New(rand.NewPCG(cfg.seed, uint64(i)+1<<40)),
			sh: &r.m.shards[i%len(r.m.shards)],
		}
		widx[t]++
		ports := len(r.ports[t])
		switch t {
		case tEnvoy:
			pc := envoy[w.widx%ports]
			w.envoy = pc[(w.widx/ports)%len(pc)]
			w.ev = newEnvoyMsgs()
		default:
			pc := rts[t][w.widx%ports]
			w.http = pc[(w.widx/ports)%len(pc)]
		}
		if cfg.sessions >= cfg.concurrency {
			for b := i; b < cfg.sessions; b += cfg.concurrency {
				w.slots = append(w.slots, &slot{base: b, alt: b%2 == 1})
			}
		} else {
			b := i % cfg.sessions
			w.slots = []*slot{{base: b, alt: b%2 == 1}}
		}
		for _, s := range w.slots {
			w.newGeneration(s, true)
		}
		r.workers[i] = w
	}
	return nil
}

// ---- scenarios ----

// runSteady ramps every worker in over -ramp, holds for -duration, and drains.
func (r *runner) runSteady(ctx context.Context) {
	cfg := r.cfg
	var stop atomic.Bool
	var wg sync.WaitGroup
	r.setPhase("ramp")
	rampStart := time.Now()
	n := len(r.workers)
	for i, w := range r.workers {
		due := rampStart.Add(time.Duration(int64(cfg.ramp) * int64(i) / int64(n)))
		if d := time.Until(due); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx, &stop)
		}()
	}
	r.steadyFrom = time.Now()
	r.setPhase(map[bool]string{true: "soak", false: "steady"}[cfg.scenario == "soak"])

	var profWG sync.WaitGroup
	var profOut []string
	var profMu sync.Mutex
	if cfg.profileDir != "" {
		r.startProfiles(ctx, &profWG, &profMu, &profOut)
	}
	select {
	case <-time.After(cfg.duration):
	case <-ctx.Done():
	}
	profWG.Wait()
	if cfg.profileDir != "" {
		// Under load still: these show the steady state, not the drained server.
		r.setPhase("profiles")
		for _, p := range [][2]string{
			{"heap", "heap.pprof"}, {"allocs", "allocs.pprof"}, {"goroutine?debug=1", "goroutine.txt"},
			{"mutex", "mutex.pprof"}, {"block", "block.pprof"},
		} {
			profOut = append(profOut, fetchProfile(context.Background(), cfg.admin, cfg.profileDir, p[0], p[1], 60*time.Second))
		}
	}
	r.steadyTo = time.Now()
	r.setPhase("drain")
	stop.Store(true)
	if !waitTimeout(&wg, cfg.timeout+10*time.Second) {
		r.notes = append(r.notes, fmt.Sprintf("drain: workers still busy %s after stop (in-flight %d)", cfg.timeout+10*time.Second, r.m.inflight()))
	}
	if len(profOut) > 0 {
		r.notes = append(r.notes, "profiles in "+cfg.profileDir+":\n"+strings.Join(profOut, "\n"))
	}
}

func (r *runner) startProfiles(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, out *[]string) {
	cfg := r.cfg
	if err := os.MkdirAll(cfg.profileDir, 0o755); err != nil {
		*out = append(*out, "  mkdir failed: "+err.Error())
		return
	}
	// Start 2s into steady state; fit inside it.
	avail := int((cfg.duration - 4*time.Second) / time.Second)
	cpu, trace := min(20, avail), min(5, avail)
	if cpu < 1 {
		*out = append(*out, "  cpu.pprof/trace.out skipped: -duration too short for a profile window (need > 5s)")
		return
	}
	for _, p := range []struct {
		path, file string
		secs       int
	}{{"profile?seconds=", "cpu.pprof", cpu}, {"trace?seconds=", "trace.out", trace}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
			s := fetchProfile(ctx, cfg.admin, cfg.profileDir, p.path+strconv.Itoa(p.secs), p.file, time.Duration(p.secs+30)*time.Second)
			mu.Lock()
			*out = append(*out, s)
			mu.Unlock()
		}()
	}
}

// waitChan is closed when wg's count reaches zero.
func waitChan(wg *sync.WaitGroup) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	select {
	case <-waitChan(wg):
		return true
	case <-time.After(d):
		return false
	}
}

// runBurst warms up with a few workers, then repeatedly releases every worker's turn at the same
// instant and measures how long the server takes to clear the burst.
func (r *runner) runBurst(ctx context.Context) {
	cfg := r.cfg
	var stop atomic.Bool
	var wg sync.WaitGroup
	r.setPhase("warmup")
	warm := min(64, len(r.workers))
	for _, w := range r.workers[:warm] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx, &stop)
		}()
	}
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
	stop.Store(true)
	wg.Wait()

	r.steadyFrom = time.Now()
	deadline := r.steadyFrom.Add(cfg.duration)
	for round := 1; ctx.Err() == nil && (round == 1 || time.Now().Before(deadline)); round++ {
		r.setPhase("burst " + strconv.Itoa(round))
		gate := make(chan struct{})
		var bw sync.WaitGroup
		done := make([]time.Duration, len(r.workers))
		var release time.Time
		errBefore := r.m.errTotal()
		for i, w := range r.workers {
			bw.Add(1)
			go func() {
				defer bw.Done()
				<-gate
				w.step(ctx)
				done[i] = time.Since(release)
			}()
		}
		time.Sleep(200 * time.Millisecond) // every goroutine parked on the gate
		release = time.Now()
		close(gate)
		var peak int64
		finished := waitChan(&bw)
	sample:
		for {
			// The 1s timeline misses a burst's peak; sample it every millisecond.
			peak = max(peak, r.m.inflight())
			select {
			case <-time.After(time.Millisecond):
			case <-finished:
				break sample
			}
		}
		r.maxInfl = max(r.maxInfl, peak)
		clear := time.Since(release)
		var s snap
		for _, d := range done {
			s.b[bucketOf(int64(d))]++
			s.n++
			s.max = max(s.max, int64(d))
		}
		line := fmt.Sprintf("burst %d: %d turns released at once; peak in-flight %d; cleared in %s; turn completion p50 %s p90 %s p99 %s; errors %d",
			round, len(r.workers), peak, clear.Round(time.Millisecond), ms(s.quantile(.5)), ms(s.quantile(.9)), ms(s.quantile(.99)), r.m.errTotal()-errBefore)
		fmt.Println(line)
		r.bursts = append(r.bursts, line)
		r.setPhase("pause")
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
		}
	}
	r.steadyTo = time.Now()
}

// ---- timeline ----

// timeline prints a line per second and, in the soak scenario, a heap/goroutine trend every 5s.
// The returned func stops it.
func (r *runner) timeline(ctx context.Context) func() {
	stopc := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var prevHist snap
		prevSrv := r.stats.fetch(ctx)
		prevErr := uint64(0)
		prevCPU := r.cpuStart
		prevAt := time.Now()
		for i := 1; ; i++ {
			select {
			case <-stopc:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			now := time.Now()
			el := now.Sub(prevAt).Seconds()
			prevAt = now
			var h snap
			for t := range nTargets {
				for _, p := range []phase{phRequest, phResponse} {
					for j := range r.m.shards {
						h.add(&r.m.shards[j].hist[t][p])
					}
				}
			}
			iv := h.minus(&prevHist)
			prevHist = h
			errs := r.m.errTotal()
			inf := r.m.inflight()
			r.maxInfl = max(r.maxInfl, inf)
			ph := *r.phase.Load()
			if ph == "steady" || ph == "soak" {
				r.inflightS = append(r.inflightS, float64(inf))
			}
			cpu := getRusage()
			cpuPct := float64(cpu.cpu-prevCPU.cpu) / float64(time.Second) / el * 100
			prevCPU = cpu

			srv := r.stats.fetch(ctx)
			var sb strings.Builder
			if srv.err != nil || prevSrv.err != nil {
				fmt.Fprintf(&sb, "srv stats unavailable: %v", srv.err)
			} else {
				d := func(k string) float64 { return (srv.v[k] - prevSrv.v[k]) / el }
				fmt.Fprintf(&sb, "srv calls/s %.0f blk/s %.0f fail/s %.0f drop/s %.0f heap %s gor %.0f gc +%.0f pause +%.1fms",
					d("calls"), d("blocked"), d("failed"), (sumPrefix(srv.v, "dropped")-sumPrefix(prevSrv.v, "dropped"))/el,
					bytesStr(srv.v["runtime.heap_alloc"]), srv.v["runtime.goroutines"],
					srv.v["runtime.num_gc"]-prevSrv.v["runtime.num_gc"], srv.v["runtime.gc_pause_total_ms"]-prevSrv.v["runtime.gc_pause_total_ms"])
			}
			if srv.err == nil {
				prevSrv = srv
				if lim := r.cfg.abortHeapMiB; lim > 0 && srv.v["runtime.heap_inuse"] > float64(lim)*(1<<20) {
					msg := fmt.Sprintf("ABORTED: server heap_inuse %s exceeded -abort-heap-mib %d", bytesStr(srv.v["runtime.heap_inuse"]), lim)
					fmt.Println(msg)
					r.aborted.Store(&msg)
					r.abort()
				}
			}
			fmt.Printf("[%5.0fs %-8s] inflight %6d | calls/s %7.0f err/s %5.0f | p50 %s p99 %s | %s | cli cpu %4.0f%% (%.0f%% of %d)\n",
				now.Sub(r.start).Seconds(), ph, inf, float64(iv.n)/el, float64(errs-prevErr)/el,
				ms(iv.quantile(.5)), ms(iv.quantile(.99)), sb.String(), cpuPct, cpuPct/float64(runtime.GOMAXPROCS(0)), runtime.GOMAXPROCS(0))
			prevErr = errs
			if r.cfg.scenario == "soak" && i%5 == 0 && srv.err == nil {
				fmt.Printf("soak t=%.0fs heap_alloc %s heap_inuse %s sys %s goroutines %.0f num_gc %.0f\n",
					now.Sub(r.start).Seconds(), bytesStr(srv.v["runtime.heap_alloc"]), bytesStr(srv.v["runtime.heap_inuse"]),
					bytesStr(srv.v["runtime.sys"]), srv.v["runtime.goroutines"], srv.v["runtime.num_gc"])
			}
		}
	}()
	return func() {
		close(stopc)
		<-done
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.3fms", float64(d)/float64(time.Millisecond)) }

// ---- report ----

func (r *runner) report() {
	cfg := r.cfg
	m := r.m
	out := os.Stdout
	wall := time.Since(r.start)
	steady := r.steadyTo.Sub(r.steadyFrom)
	cpu := getRusage()

	fmt.Fprintf(out, "\n==== beacon-gateway stress summary ====\n")
	fmt.Fprintf(out, "scenario=%s target=%s proto=%s concurrency=%d conns/port litellm=%d portkey=%d envoy=%d ramp=%s duration=%s sessions=%d turn-max=%d bad-ratio=%g timeout=%s seed=%d run=%s\n",
		cfg.scenario, cfg.target, cfg.proto, cfg.concurrency, r.conns[tLiteLLM], r.conns[tPortkey], r.conns[tEnvoy],
		cfg.ramp, cfg.duration, cfg.sessions, cfg.turnMax, cfg.badRatio, cfg.timeout, cfg.seed, r.runID)
	var count [nTargets]int
	for _, w := range r.workers {
		count[w.tgt]++
	}
	fmt.Fprintf(out, "workers: litellm=%d (%d ports) portkey=%d (%d ports) envoy=%d (%d ports); client GOMAXPROCS=%d NumCPU=%d\n",
		count[tLiteLLM], len(r.ports[tLiteLLM]), count[tPortkey], len(r.ports[tPortkey]), count[tEnvoy], len(r.ports[tEnvoy]),
		runtime.GOMAXPROCS(0), runtime.NumCPU())
	for _, n := range r.notes {
		fmt.Fprintln(out, "note:", n)
	}
	if msg := r.aborted.Load(); msg != nil {
		fmt.Fprintln(out, "!!!", *msg)
	}
	var sessions uint64
	for i := range m.shards {
		sessions += m.shards[i].sessions.Load()
	}
	var meanInfl float64
	for _, x := range r.inflightS {
		meanInfl += x
	}
	if len(r.inflightS) > 0 {
		meanInfl /= float64(len(r.inflightS))
	}
	fmt.Fprintf(out, "wall %s (measured window %s); session generations started %d\n", wall.Round(time.Millisecond), steady.Round(time.Millisecond), sessions)
	if len(r.inflightS) > 0 {
		fmt.Fprintf(out, "in-flight: target %d, max sampled %d, mean over steady state %.0f\n", cfg.concurrency, r.maxInfl, meanInfl)
	} else {
		fmt.Fprintf(out, "in-flight: target %d, max sampled %d\n", cfg.concurrency, r.maxInfl)
	}

	var calls uint64
	for t := range nTargets {
		for _, p := range []phase{phRequest, phResponse} {
			s := m.merged(target(t), p)
			calls += s.n
		}
	}
	fmt.Fprintf(out, "completed adapter calls %d, overall %.0f calls/s\n", calls, float64(calls)/wall.Seconds())
	for _, b := range r.bursts {
		fmt.Fprintln(out, b)
	}

	fmt.Fprintf(out, "\nlatency of successful calls (whole run):\n")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "target\tphase\tcount\tmean\tp50\tp90\tp99\tp99.9\tmax\t")
	for t := range nTargets {
		for p := range nPhases {
			s := m.merged(target(t), phase(p))
			if s.n == 0 {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t\n", target(t), phaseNames[p], s.n,
				ms(s.mean()), ms(s.quantile(.5)), ms(s.quantile(.9)), ms(s.quantile(.99)), ms(s.quantile(.999)), ms(time.Duration(s.max)))
		}
	}
	tw.Flush()

	fmt.Fprintf(out, "\nerrors:\n")
	nerr := 0
	m.mu.Lock()
	samples := m.samples
	m.mu.Unlock()
	line := func(key string, n uint64) {
		nerr++
		fmt.Fprintf(out, "  %-52s %9d  e.g. %s\n", key, n, oneLine(samples[key]))
	}
	for t := range nTargets {
		tn := target(t).String()
		var errs [nErrClasses]uint64
		var st [600]uint64
		var gc [17]uint64
		var vi [nViolations]uint64
		for i := range m.shards {
			sh := &m.shards[i]
			for c := range errs {
				errs[c] += sh.errs[t][c].Load()
			}
			for c := range st {
				st[c] += sh.status[t][c].Load()
			}
			for c := range gc {
				gc[c] += sh.grpc[t][c].Load()
			}
			for c := range vi {
				vi[c] += sh.viol[t][c].Load()
			}
		}
		for c, n := range errs {
			if n > 0 {
				line(tn+" "+errNames[c], n)
			}
		}
		for c, n := range st {
			if n > 0 {
				line(fmt.Sprintf("%s http %d", tn, c), n)
			}
		}
		for c, n := range gc {
			if n > 0 {
				line(tn+" grpc "+grpcCodeName(c), n)
			}
		}
		for c, n := range vi {
			if n > 0 {
				line(tn+" "+violationNames[c], n)
			}
		}
	}
	if nerr == 0 {
		fmt.Fprintln(out, "  none")
	}

	allow := m.totals(func(s *shard) *[nTargets]atomic.Uint64 { return &s.allow })
	block := m.totals(func(s *shard) *[nTargets]atomic.Uint64 { return &s.block })
	badBlock := m.totals(func(s *shard) *[nTargets]atomic.Uint64 { return &s.badBlock })
	fa := m.totals(func(s *shard) *[nTargets]atomic.Uint64 { return &s.falseAllw })
	fb := m.totals(func(s *shard) *[nTargets]atomic.Uint64 { return &s.falseBlk })
	un := m.totals(func(s *shard) *[nTargets]atomic.Uint64 { return &s.unenf })
	fmt.Fprintf(out, "\nverdicts:\n")
	var totalFA, totalFB uint64
	for t := range nTargets {
		if allow[t]+block[t] == 0 {
			continue
		}
		fmt.Fprintf(out, "  %-8s allow %d block %d | bad calls blocked %d, bad replies unenforceable (STREAMED) %d | false-allow %d false-block %d\n",
			target(t), allow[t], block[t], badBlock[t], un[t], fa[t], fb[t])
		totalFA += fa[t]
		totalFB += fb[t]
	}
	if totalFA+totalFB > 0 {
		fmt.Fprintf(out, "!!!!!!!! CORRECTNESS RED FLAG: false-allow %d (bad call allowed), false-block %d (benign call blocked) !!!!!!!!\n", totalFA, totalFB)
	} else {
		fmt.Fprintf(out, "correctness: false-allow 0, false-block 0\n")
	}

	if lines := m.sortedChaos(); len(lines) > 0 {
		fmt.Fprintf(out, "\nchaos outcomes (unexpected %d):\n", m.chaosUn.Load())
		for _, l := range lines {
			fmt.Fprintln(out, "  "+l)
		}
	}

	r.reportServer(out)

	c := cpu.cpu - r.cpuStart.cpu
	pct := float64(c) / float64(wall) * 100
	fmt.Fprintf(out, "\nclient: cpu %s over %s wall = %.0f%% of one core (%.1f%% of GOMAXPROCS %d); max RSS %s\n",
		c.Round(time.Millisecond), wall.Round(time.Millisecond), pct, pct/float64(runtime.GOMAXPROCS(0)), runtime.GOMAXPROCS(0), bytesStr(float64(cpu.maxRSS)))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func grpcCodeName(c int) string {
	names := []string{"OK", "Canceled", "Unknown", "InvalidArgument", "DeadlineExceeded", "NotFound", "AlreadyExists",
		"PermissionDenied", "ResourceExhausted", "FailedPrecondition", "Aborted", "OutOfRange", "Unimplemented",
		"Internal", "Unavailable", "DataLoss", "Unauthenticated"}
	if c < len(names) {
		return names[c]
	}
	return strconv.Itoa(c)
}

func (r *runner) reportServer(out *os.File) {
	all := r.stats.valid()
	fmt.Fprintf(out, "\nserver /stats (%d samples):\n", len(all))
	if len(all) < 2 {
		fmt.Fprintln(out, "  not enough samples")
		return
	}
	first, last := all[0], all[len(all)-1]
	var peak = map[string]float64{}
	for _, s := range all {
		for k, v := range s.v {
			peak[k] = max(peak[k], v)
		}
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "field\tfirst\tlast\tdelta\tpeak\t")
	for _, k := range sortedKeys(last.v) {
		f, l := first.v[k], last.v[k]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", k, num(k, f), num(k, l), num(k, l-f), num(k, peak[k]))
	}
	tw.Flush()
	if r.cfg.scenario == "soak" {
		var xs, heap, gor []float64
		for _, s := range all {
			if s.at.Before(r.steadyFrom) || s.at.After(r.steadyTo) {
				continue
			}
			xs = append(xs, s.at.Sub(r.steadyFrom).Minutes())
			heap = append(heap, s.v["runtime.heap_alloc"])
			gor = append(gor, s.v["runtime.goroutines"])
		}
		fmt.Fprintf(out, "soak trend over steady state (%d samples, least squares): heap_alloc %s/min, goroutines %+.1f/min\n",
			len(xs), signedBytes(slope(xs, heap)), slope(xs, gor))
	}
}

func num(k string, v float64) string {
	if strings.Contains(k, "heap") || strings.HasSuffix(k, ".sys") || strings.Contains(k, "bytes") {
		return signedBytes(v)
	}
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}

func signedBytes(v float64) string {
	if v < 0 {
		return "-" + bytesStr(-v)
	}
	return bytesStr(v)
}
