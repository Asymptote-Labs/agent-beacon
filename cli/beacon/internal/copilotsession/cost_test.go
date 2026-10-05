package copilotsession

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// These fixtures follow the shape Copilot CLI commits to
// ~/.copilot/session-state/<id>/events.jsonl: ISO 8601 record timestamps,
// session.start carrying startTime, and session.shutdown carrying the
// session-wide cumulative totalNanoAiu next to per-model modelMetrics and
// sessionStartTime in Unix milliseconds.

const costSessionID = "4b0c9d52-6a1f-4e57-9d1e-2f7a3c8e6b10"

func costStart(startISO string) string {
	return `{"id":"e-start","parentId":null,"timestamp":"` + startISO + `","type":"session.start","data":{"sessionId":"` + costSessionID + `","version":1,"producer":"copilot-agent","copilotVersion":"1.0.40","startTime":"` + startISO + `","selectedModel":"claude-sonnet-4.6","context":{"cwd":"/repo","gitRoot":"/repo","repository":"Asymptote-Labs/agent-beacon","branch":"main"}}}`
}

func costPrompt(id, ts string) string {
	return `{"id":"` + id + `","timestamp":"` + ts + `","type":"user.message","data":{"content":"fix the failing test"}}`
}

func costResume(ts string) string {
	return `{"id":"e-resume","timestamp":"` + ts + `","type":"session.resume","data":{"resumeTime":"` + ts + `","eventCount":12,"selectedModel":"claude-sonnet-4.6","context":{"cwd":"/repo"}}}`
}

// costShutdown renders a session.shutdown whose modelMetrics holds one entry
// per model (model name -> input tokens) and whose session-wide total is
// totalNanoAiu.
func costShutdown(id, ts string, startMS string, totalNanoAiu string, models map[string]string) string {
	var metrics []string
	for model, input := range models {
		metrics = append(metrics, `"`+model+`":{"requests":{"count":3,"cost":3},"usage":{"inputTokens":`+input+`,"outputTokens":0,"cacheReadTokens":0,"cacheWriteTokens":0,"reasoningTokens":0}}`)
	}
	return `{"id":"` + id + `","timestamp":"` + ts + `","type":"session.shutdown","data":{"shutdownType":"routine","sessionStartTime":` + startMS + `,"totalApiDurationMs":4200,"totalPremiumRequests":3,"totalNanoAiu":` + totalNanoAiu + `,"codeChanges":{"linesAdded":4,"linesRemoved":1,"filesModified":["main.go"]},"currentModel":"claude-sonnet-4.6","modelMetrics":{` + strings.Join(metrics, ",") + `}}}`
}

// 2026-06-10T09:00:00Z, after the usage-based billing cutover.
const (
	postCutoverISO = "2026-06-10T09:00:00.000Z"
	postCutoverMS  = "1781082000000"
	// 2026-05-20T09:00:00Z, billed per premium request.
	preCutoverISO = "2026-05-20T09:00:00.000Z"
	preCutoverMS  = "1779267600000"
)

func writeCostSession(t *testing.T, root string, lines []string) string {
	t.Helper()
	dir := filepath.Join(root, "session-state", costSessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, EventsFile)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mapCostSession(t *testing.T, root string) []MappedEvent {
	t.Helper()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs = %v, err = %v", refs, err)
	}
	records, _, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	return MapSession(refs[0], records, MapOptions{})
}

type costEvent struct {
	model string
	cost  float64
	raw   map[string]interface{}
}

func costEvents(events []MappedEvent) []costEvent {
	var out []costEvent
	for _, item := range events {
		ev := item.Event
		if ev.GenAI == nil || ev.GenAI.Usage == nil || ev.GenAI.Usage.CostUSD == nil {
			continue
		}
		raw, _ := ev.Raw["copilot"].(map[string]interface{})
		out = append(out, costEvent{model: ev.Model, cost: *ev.GenAI.Usage.CostUSD, raw: raw})
	}
	return out
}

func TestShutdownTotalNanoAiuIsRecordedAsReportedCost(t *testing.T) {
	root := t.TempDir()
	writeCostSession(t, root, []string{
		costStart(postCutoverISO),
		costPrompt("u1", "2026-06-10T09:00:05.000Z"),
		// 2.5 AI credits = $0.025.
		costShutdown("x1", "2026-06-10T09:10:00.000Z", postCutoverMS, "2500000000", map[string]string{"claude-sonnet-4.6": "1200"}),
	})
	got := costEvents(mapCostSession(t, root))
	if len(got) != 1 {
		t.Fatalf("cost events = %+v, want exactly one", got)
	}
	if got[0].cost != 0.025 {
		t.Fatalf("cost = %v, want 0.025", got[0].cost)
	}
	// One model in the shutdown: the session total is that model's, so it
	// rides that model's usage event and stays attributable by model.
	if got[0].model != "claude-sonnet-4.6" {
		t.Fatalf("cost model = %q, want claude-sonnet-4.6", got[0].model)
	}
	if got[0].raw["cumulative_total_nano_aiu"] == nil || got[0].raw["nano_aiu_delta"] == nil {
		t.Fatalf("raw nano-AIU values missing: %+v", got[0].raw)
	}
}

func TestResumedSessionCostIsDifferencedAcrossShutdowns(t *testing.T) {
	root := t.TempDir()
	writeCostSession(t, root, []string{
		costStart(postCutoverISO),
		costPrompt("u1", "2026-06-10T09:00:05.000Z"),
		costShutdown("x1", "2026-06-10T09:10:00.000Z", postCutoverMS, "2500000000", map[string]string{"claude-sonnet-4.6": "1200"}),
		costResume("2026-06-10T11:00:00.000Z"),
		costPrompt("u2", "2026-06-10T11:00:05.000Z"),
		// Cumulative: 4.1 credits in total, 1.6 of them new.
		costShutdown("x2", "2026-06-10T11:20:00.000Z", postCutoverMS, "4100000000", map[string]string{"claude-sonnet-4.6": "2000"}),
		// Repeated shutdown with no new spend must add nothing.
		costShutdown("x3", "2026-06-10T11:20:01.000Z", postCutoverMS, "4100000000", map[string]string{"claude-sonnet-4.6": "2000"}),
	})
	got := costEvents(mapCostSession(t, root))
	if len(got) != 2 {
		t.Fatalf("cost events = %+v, want two (one per shutdown with new spend)", got)
	}
	if got[0].cost != 0.025 || got[1].cost != 0.016 {
		t.Fatalf("costs = %v, %v, want 0.025, 0.016", got[0].cost, got[1].cost)
	}
}

func TestShutdownCostResetRebasesWithoutOverReporting(t *testing.T) {
	root := t.TempDir()
	writeCostSession(t, root, []string{
		costStart(postCutoverISO),
		costShutdown("x1", "2026-06-10T09:10:00.000Z", postCutoverMS, "3000000000", map[string]string{"claude-sonnet-4.6": "1200"}),
		costResume("2026-06-10T11:00:00.000Z"),
		// The cumulative total went backwards. Nothing is emitted for it,
		// and the lower value becomes the baseline.
		costShutdown("x2", "2026-06-10T11:20:00.000Z", postCutoverMS, "1000000000", map[string]string{"claude-sonnet-4.6": "1200"}),
		costResume("2026-06-10T12:00:00.000Z"),
		costShutdown("x3", "2026-06-10T12:20:00.000Z", postCutoverMS, "1700000000", map[string]string{"claude-sonnet-4.6": "1200"}),
	})
	got := costEvents(mapCostSession(t, root))
	if len(got) != 2 {
		t.Fatalf("cost events = %+v, want two", got)
	}
	if got[0].cost != 0.03 || got[1].cost != 0.007 {
		t.Fatalf("costs = %v, %v, want 0.03, 0.007", got[0].cost, got[1].cost)
	}
}

func TestPreCutoverSessionRecordsNoCost(t *testing.T) {
	root := t.TempDir()
	writeCostSession(t, root, []string{
		costStart(preCutoverISO),
		costPrompt("u1", "2026-05-20T09:00:05.000Z"),
		costShutdown("x1", "2026-05-20T09:10:00.000Z", preCutoverMS, "2500000000", map[string]string{"claude-sonnet-4.6": "1200"}),
		// Resumed after the cutover: still a premium-request session.
		costResume("2026-06-03T10:00:00.000Z"),
		costShutdown("x2", "2026-06-03T10:10:00.000Z", preCutoverMS, "5000000000", map[string]string{"claude-sonnet-4.6": "2400"}),
	})
	events := mapCostSession(t, root)
	if got := costEvents(events); len(got) != 0 {
		t.Fatalf("pre-cutover session recorded cost: %+v", got)
	}
	// Tokens are still recorded, and the raw billing total is kept.
	var sawRaw bool
	for _, item := range events {
		raw, _ := item.Event.Raw["copilot"].(map[string]interface{})
		if raw["type"] == "session.shutdown" && raw["cumulative_total_nano_aiu"] != nil {
			sawRaw = true
		}
	}
	if !sawRaw {
		t.Fatal("raw cumulative_total_nano_aiu missing on pre-cutover shutdown usage")
	}
}

func TestSessionWithoutKnownStartRecordsNoCost(t *testing.T) {
	root := t.TempDir()
	// No session.start and no sessionStartTime: the billing regime is unknown.
	writeCostSession(t, root, []string{
		`{"id":"x1","timestamp":"2026-06-10T09:10:00.000Z","type":"session.shutdown","data":{"shutdownType":"routine","totalNanoAiu":2500000000,"modelMetrics":{"claude-sonnet-4.6":{"requests":{"count":1,"cost":1},"usage":{"inputTokens":10,"outputTokens":0}}}}}`,
	})
	if got := costEvents(mapCostSession(t, root)); len(got) != 0 {
		t.Fatalf("cost recorded without a known session start: %+v", got)
	}
}

func TestMultiModelShutdownCountsSessionCostOnce(t *testing.T) {
	root := t.TempDir()
	writeCostSession(t, root, []string{
		costStart(postCutoverISO),
		costShutdown("x1", "2026-06-10T09:10:00.000Z", postCutoverMS, "7300000000", map[string]string{
			"claude-sonnet-4.6": "1200",
			"gpt-5.3-codex":     "800",
			"claude-haiku-4.5":  "300",
		}),
	})
	events := mapCostSession(t, root)
	got := costEvents(events)
	if len(got) != 1 {
		t.Fatalf("cost events = %+v, want exactly one for a multi-model shutdown", got)
	}
	if got[0].cost != 0.073 {
		t.Fatalf("cost = %v, want 0.073", got[0].cost)
	}
	// The total is session-level, so it is not pinned on any one model.
	if got[0].model != "" {
		t.Fatalf("multi-model session cost attributed to model %q", got[0].model)
	}
	var modelUsage int
	for _, item := range events {
		if item.Event.Event.Action == "token.usage" && item.Event.Model != "" {
			modelUsage++
		}
	}
	if modelUsage != 3 {
		t.Fatalf("per-model token usage events = %d, want 3", modelUsage)
	}
}

func TestNanoAiuParsingIsExact(t *testing.T) {
	for _, tc := range []struct {
		in   interface{}
		want int64
		ok   bool
	}{
		{float64(2500000000), 2500000000, true},
		{json.Number("123456789012"), 123456789012, true},
		{"42", 42, true},
		{float64(10.4), 10, true},
		{float64(-1), 0, false},
		{nil, 0, false},
		{"abc", 0, false},
	} {
		got, ok := nanoAiuValue(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("nanoAiuValue(%#v) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if got := nanoAiuToUSD(1); got != 1e-11 {
		t.Fatalf("1 nano-AIU = %v USD, want 1e-11", got)
	}
}

func readRuntimeLog(t *testing.T, path string) []schema.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []schema.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("decode runtime log line: %v", err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCopilotCostReachesTokenReportExactlyOnceAcrossResyncs(t *testing.T) {
	root := t.TempDir()
	path := writeCostSession(t, root, []string{
		costStart(postCutoverISO),
		costPrompt("u1", "2026-06-10T09:00:05.000Z"),
		costShutdown("x1", "2026-06-10T09:10:00.000Z", postCutoverMS, "2500000000", map[string]string{"claude-sonnet-4.6": "1200"}),
	})
	statePath := filepath.Join(root, "state.json")
	logPath := filepath.Join(root, "runtime.jsonl")
	opts := CollectOptions{CopilotDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
	if _, err := CollectOnce(opts); err != nil {
		t.Fatal(err)
	}
	report := tokens.Aggregate(readRuntimeLog(t, logPath), tokens.Options{})
	if report.Totals.CostUSD != float64(2500000000)/1e11 {
		t.Fatalf("totals.cost_usd after first sync = %v, want %v", report.Totals.CostUSD, float64(2500000000)/1e11)
	}

	// Copilot resumes the session and appends a second, cumulative shutdown.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		costResume("2026-06-10T11:00:00.000Z"),
		costPrompt("u2", "2026-06-10T11:00:05.000Z"),
		costShutdown("x2", "2026-06-10T11:20:00.000Z", postCutoverMS, "4000000000", map[string]string{"claude-sonnet-4.6": "2000", "gpt-5.3-codex": "500"}),
	} {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	if _, err := CollectOnce(opts); err != nil {
		t.Fatal(err)
	}
	// A re-run with nothing new must not add cost.
	if summary, err := CollectOnce(opts); err != nil || summary.EventsEmitted != 0 {
		t.Fatalf("idle re-sync emitted %d events, err %v", summary.EventsEmitted, err)
	}
	report = tokens.Aggregate(readRuntimeLog(t, logPath), tokens.Options{})
	if report.Totals.CostUSD != float64(4000000000)/1e11 {
		t.Fatalf("totals.cost_usd after resume = %v, want %v", report.Totals.CostUSD, float64(4000000000)/1e11)
	}
	if report.Totals.InputTokens != 2500 {
		t.Fatalf("totals.input_tokens = %d, want 2500", report.Totals.InputTokens)
	}
}
