// Command load replays synthetic Claude-Code-shaped sessions against one in-process pipeline, to
// measure the shared pipeline under many concurrent agents. Adapters are thin HTTP/gRPC shims and
// are not exercised.
//
// Each agent runs one session of -turns turns. A turn is two calls, as a gateway sees them: the
// request (the whole conversation so far, as harnesses resend it) and the response (the model's
// reply with one tool_use). Bodies are built outside the timed region; only Handle is timed.
//
//	go run ./scripts/load -agents 50 -turns 100
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/events"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/pipeline"
)

type config struct {
	agents, turns, resultBytes int
	seed                       uint64
	cpus                       int
	interval                   time.Duration
}

func main() {
	var cfg config
	flag.IntVar(&cfg.agents, "agents", 50, "concurrent agents, one session each")
	flag.IntVar(&cfg.turns, "turns", 100, "turns per session (two calls each)")
	flag.IntVar(&cfg.resultBytes, "result-bytes", 3000, "bytes per tool result")
	flag.Uint64Var(&cfg.seed, "seed", 1, "seed for the synthetic sessions")
	flag.IntVar(&cfg.cpus, "cpus", 0, "GOMAXPROCS (0 = all)")
	flag.DurationVar(&cfg.interval, "turn-interval", 10*time.Second, "simulated time between turns (Call.Time)")
	flag.Parse()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
}

// Bounds on the replay's size: a replayed body grows with every turn by about -result-bytes.
const (
	maxAgents      = 100_000
	maxTurns       = 100_000
	maxResultBytes = 16 << 20
	// maxBody bounds a replayed request body's parts; a session that would grow past it ends early.
	maxBody = 512 << 20
)

// bodyEnd closes a request body after its messages.
var bodyEnd = []byte("]}")

func run(cfg config) error {
	if cfg.agents < 1 || cfg.turns < 1 || cfg.resultBytes < 1 {
		return fmt.Errorf("-agents, -turns and -result-bytes must be positive")
	}
	if cfg.agents > maxAgents || cfg.turns > maxTurns || cfg.resultBytes > maxResultBytes {
		return fmt.Errorf("-agents, -turns and -result-bytes are limited to %d, %d and %d", maxAgents, maxTurns, maxResultBytes)
	}
	if cfg.cpus > 0 {
		runtime.GOMAXPROCS(cfg.cpus)
	}
	// Keep stderr to errors (failed analyses).
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	plans := make([]plan, cfg.agents)
	for i := range plans {
		plans[i] = newPlan(cfg, i)
	}
	sink := &countSink{kinds: map[string]int{}}
	p := pipeline.New(pipeline.Config{
		Analyzers: analyzers(),
		Routes:    []pipeline.Route{{Name: "load-counter", Sink: sink}},
	})

	prefix := synth.AnthropicPrefix("user_load_account_0001")
	heapBefore := liveHeap()

	results := make([]agentResult, cfg.agents)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range plans {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			results[i] = runAgent(cfg, p, prefix, plans[i])
		}(i)
	}
	start := time.Now()
	close(gate)
	wg.Wait()
	wall := time.Since(start)
	p.Close()
	stats := p.Stats()

	heapAfter := liveHeap()
	runtime.KeepAlive(p)

	report(cfg, reportInput{
		results: results, sink: sink, wall: wall,
		heapBefore: heapBefore, heapAfter: heapAfter, stats: stats,
	})
	return nil
}

// analyzers are the analyzers the replay runs: the events analyzer, as the gateway runs it.
func analyzers() []pipeline.Analyzer {
	ev, err := events.New(events.Config{})
	if err != nil {
		panic(err)
	}
	return []pipeline.Analyzer{ev}
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// plan is a session's identity.
type plan struct {
	index   int
	session string
}

func newPlan(cfg config, i int) plan {
	r := rand.New(rand.NewPCG(cfg.seed, uint64(i)))
	return plan{index: i, session: fmt.Sprintf("load-%04d-%08x", i, r.Uint32())}
}

// callID names a call; both phases of one turn share it, as gateways do.
func callID(session string, turn int) string { return session + "/turn-" + strconv.Itoa(turn) }

// ---- agents ----

const phases = 2 // 0 request, 1 response

type agentResult struct {
	lat     [phases][]time.Duration // indexed by turn-1
	build   time.Duration
	blocked [phases]int
	allowed [phases]int
	lastReq int
}

func runAgent(cfg config, p *pipeline.Pipeline, prefix []byte, pl plan) agentResult {
	r := rand.New(rand.NewPCG(cfg.seed^0x9e3779b97f4a7c15, uint64(pl.index)))
	res := agentResult{lat: [phases][]time.Duration{make([]time.Duration, cfg.turns), make([]time.Duration, cfg.turns)}}
	ctx := context.Background()
	headers := map[string]string{"x-claude-code-session-id": pl.session}
	base := time.Now()

	t0 := time.Now()
	msgs := synth.UserMessage(synth.FirstPrompt)
	res.build += time.Since(t0)

	for k := 1; k <= cfg.turns; k++ {
		t0 = time.Now()
		if len(prefix) > maxBody || len(msgs) > maxBody {
			fmt.Fprintf(os.Stderr, "load: session %s ends at turn %d: its body would exceed %d bytes\n", pl.session, k, maxBody)
			break
		}
		body := slices.Concat(prefix, msgs, bodyEnd)
		tu := synth.RandomTool(r, synth.ToolID(r))
		res.build += time.Since(t0)
		res.lastReq = len(body)

		at := base.Add(time.Duration(k-1) * cfg.interval)
		id := callID(pl.session, k)
		res.record(0, k, p, ctx, &gateway.Call{
			Gateway: "load", ID: id, Phase: gateway.PhaseRequest, Enforce: true, Time: at, Model: synth.Model,
			Protocol: llm.AnthropicMessages, Body: body,
			Client: gateway.Client{UserAgent: synth.UserAgent, Headers: headers},
		})

		t0 = time.Now()
		text := synth.Narration(tu)
		resp := synth.AnthropicResponse(fmt.Sprintf("msg_%016x", r.Uint64()), text, tu, len(body)/4)
		res.build += time.Since(t0)
		res.record(1, k, p, ctx, &gateway.Call{
			Gateway: "load", ID: id, Phase: gateway.PhaseResponse, Enforce: true, Time: at.Add(cfg.interval / 2), Model: synth.Model,
			Protocol: llm.AnthropicMessages, Body: resp,
			Client: gateway.Client{UserAgent: synth.UserAgent, Headers: headers},
		})

		// The harness runs the tool regardless of the verdict here, so every session keeps the
		// same shape; the next request carries the call and its result.
		t0 = time.Now()
		msgs = append(msgs, ',')
		msgs = append(msgs, synth.AssistantMessage(text, tu)...)
		msgs = append(msgs, ',')
		msgs = append(msgs, synth.ResultMessage(tu, synth.ToolResult(r, tu, cfg.resultBytes))...)
		res.build += time.Since(t0)
	}
	return res
}

func (res *agentResult) record(phase, turn int, p *pipeline.Pipeline, ctx context.Context, c *gateway.Call) {
	s := time.Now()
	v := p.Handle(ctx, c)
	res.lat[phase][turn-1] = time.Since(s)
	if v.Action == gateway.Block {
		res.blocked[phase]++
	} else {
		res.allowed[phase]++
	}
}

// ---- record sink ----

// countSink counts records by kind.
type countSink struct {
	mu    sync.Mutex
	kinds map[string]int
}

func (s *countSink) Write(_ context.Context, batch []pipeline.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range batch {
		s.kinds[rec.Kind]++
	}
	return nil
}

func (s *countSink) Close() error { return nil }

// ---- report ----

type reportInput struct {
	results               []agentResult
	sink                  *countSink
	wall                  time.Duration
	heapBefore, heapAfter uint64
	stats                 any
}

var buckets = []struct {
	name   string
	lo, hi int
}{{"1-10", 1, 10}, {"11-50", 11, 50}, {"51-100", 51, 100}, {"101+", 101, math.MaxInt}}

func report(cfg config, in reportInput) {
	out := os.Stdout
	calls := cfg.agents * cfg.turns * 2
	fmt.Fprintf(out, "beacon-gateway load replay\n")
	fmt.Fprintf(out, "agents=%d turns=%d result-bytes=%d seed=%d GOMAXPROCS=%d NumCPU=%d turn-interval=%s\n",
		cfg.agents, cfg.turns, cfg.resultBytes, cfg.seed, runtime.GOMAXPROCS(0), runtime.NumCPU(), cfg.interval)
	var lastReq int
	var build time.Duration
	for _, r := range in.results {
		lastReq = max(lastReq, r.lastReq)
		build += r.build
	}
	fmt.Fprintf(out, "request body at last turn: %s; body construction (untimed): %s total, %s per call\n\n",
		bytesStr(uint64(lastReq)), build.Round(time.Millisecond), (build / time.Duration(calls)).Round(time.Microsecond))

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "phase\tturns\tcalls\tp50\tp95\tp99\tmax\t")
	for ph, name := range []string{"request", "response"} {
		for _, b := range buckets {
			if b.lo > cfg.turns {
				break
			}
			var d []time.Duration
			for _, r := range in.results {
				d = append(d, r.lat[ph][b.lo-1:min(b.hi, cfg.turns)]...)
			}
			sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t\n", name, b.name, len(d),
				ms(pct(d, .50)), ms(pct(d, .95)), ms(pct(d, .99)), ms(d[len(d)-1]))
		}
	}
	tw.Flush()

	secs := in.wall.Seconds()
	fmt.Fprintf(out, "\nwall time: %s\n", in.wall.Round(time.Millisecond))
	fmt.Fprintf(out, "throughput: %.1f calls/s, %.1f turns/s\n", float64(calls)/secs, float64(cfg.agents*cfg.turns)/secs)
	growth := int64(in.heapAfter) - int64(in.heapBefore)
	fmt.Fprintf(out, "live heap after run (after GC): %s (before run %s, growth %s, %s per session)\n",
		bytesStr(in.heapAfter), bytesStr(in.heapBefore), signedBytes(growth), signedBytes(growth/int64(cfg.agents)))

	var allowed, blocked [phases]int
	for _, r := range in.results {
		for ph := range phases {
			allowed[ph] += r.allowed[ph]
			blocked[ph] += r.blocked[ph]
		}
	}
	fmt.Fprintf(out, "\nverdicts: allow=%d block=%d (request allow=%d block=%d; response allow=%d block=%d)\n",
		allowed[0]+allowed[1], blocked[0]+blocked[1], allowed[0], blocked[0], allowed[1], blocked[1])
	s := in.sink
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(out, "pipeline stats: %+v\n", in.stats)
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "record kind\trecords")
	for _, kind := range sortedKeys(s.kinds) {
		fmt.Fprintf(tw, "%s\t%d\n", kind, s.kinds[kind])
	}
	tw.Flush()
}

func pct(d []time.Duration, q float64) time.Duration {
	i := int(math.Ceil(q*float64(len(d)))) - 1
	return d[max(0, min(i, len(d)-1))]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.3fms", float64(d)/float64(time.Millisecond)) }

func bytesStr(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func signedBytes(n int64) string {
	if n < 0 {
		return "-" + bytesStr(uint64(-n))
	}
	return bytesStr(uint64(n))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
