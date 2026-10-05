package factorysession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// usageFixture is one Factory session on disk whose settings file a test rewrites between sweeps,
// the way Droid rewrites <session-id>.settings.json with new cumulative totals after each turn.
type usageFixture struct {
	t            *testing.T
	root         string
	sessionPath  string
	settingsPath string
	statePath    string
	out          bytes.Buffer
	settingsTick time.Time
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "-repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &usageFixture{
		t:            t,
		root:         root,
		sessionPath:  filepath.Join(project, "session-1.jsonl"),
		settingsPath: filepath.Join(project, "session-1.settings.json"),
		statePath:    filepath.Join(t.TempDir(), "factory-state.json"),
		settingsTick: time.Unix(1770000000, 0),
	}
	writeFile(t, f.sessionPath,
		`{"type":"session_start","id":"session-1","title":"hi","cwd":"/repo"}`+"\n"+
			`{"type":"message","id":"u1","timestamp":"2026-05-16T22:11:04.611Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n")
	return f
}

// writeSettings replaces the settings file and moves its mtime forward a second, so every
// rewrite is visible to the collector even on filesystems with coarse timestamps.
func (f *usageFixture) writeSettings(body string) {
	f.t.Helper()
	writeFile(f.t, f.settingsPath, body)
	f.settingsTick = f.settingsTick.Add(time.Second)
	if err := os.Chtimes(f.settingsPath, f.settingsTick, f.settingsTick); err != nil {
		f.t.Fatal(err)
	}
}

func (f *usageFixture) sweep() Summary {
	f.t.Helper()
	summary, err := CollectOnce(CollectOptions{SessionsDir: f.root, StatePath: f.statePath, Print: true, Out: &f.out})
	if err != nil {
		f.t.Fatal(err)
	}
	return summary
}

func (f *usageFixture) events() []schema.Event {
	f.t.Helper()
	var events []schema.Event
	scanner := bufio.NewScanner(bytes.NewReader(f.out.Bytes()))
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			f.t.Fatal(err)
		}
		events = append(events, ev)
	}
	return events
}

func (f *usageFixture) usageEvents() []schema.Event {
	var out []schema.Event
	for _, ev := range f.events() {
		if ev.Event.Action == "token.usage" {
			out = append(out, ev)
		}
	}
	return out
}

// Factory keeps tokenUsage as the session's running total, so the sum of what Beacon emits across
// several settings rewrites must equal the final total, not the sum of every total it saw.
func TestCollectOnceEmitsFactorySettingsUsageAsDeltas(t *testing.T) {
	f := newUsageFixture(t)
	f.writeSettings(`{"model":"claude-opus-4-7","tokenUsage":{"inputTokens":100,"outputTokens":10,"cacheCreationTokens":1000,"cacheReadTokens":5000,"thinkingTokens":4,"factoryCredits":1.5}}`)
	f.sweep()
	f.writeSettings(`{"model":"claude-opus-4-7","tokenUsage":{"inputTokens":250,"outputTokens":30,"cacheCreationTokens":1200,"cacheReadTokens":12000,"thinkingTokens":9,"factoryCredits":3.25}}`)
	f.sweep()
	// A rewrite that leaves the totals alone (Droid also stores the model, active time and
	// other settings there) is not new spend.
	f.writeSettings(`{"model":"claude-opus-4-7","assistantActiveTimeMs":900,"tokenUsage":{"inputTokens":250,"outputTokens":30,"cacheCreationTokens":1200,"cacheReadTokens":12000,"thinkingTokens":9,"factoryCredits":3.25}}`)
	if summary := f.sweep(); summary.EventsEmitted != 0 {
		t.Fatalf("unchanged totals emitted %d events, want 0", summary.EventsEmitted)
	}
	f.writeSettings(`{"model":"claude-opus-4-7","tokenUsage":{"inputTokens":400,"outputTokens":75,"cacheCreationTokens":1500,"cacheReadTokens":20000,"thinkingTokens":20,"factoryCredits":6}}`)
	f.sweep()
	// A re-run over a store that has not changed emits nothing.
	if summary := f.sweep(); summary.EventsEmitted != 0 {
		t.Fatalf("idempotent re-run emitted %d events, want 0", summary.EventsEmitted)
	}

	report := tokens.Aggregate(f.events(), tokens.Options{})
	want := tokens.Usage{
		InputTokens:              400,
		OutputTokens:             75,
		CacheCreationInputTokens: 1500,
		CacheReadInputTokens:     20000,
		ReasoningOutputTokens:    20,
		Events:                   3,
	}
	if report.Totals != want {
		t.Fatalf("aggregated totals = %+v, want the final cumulative value %+v", report.Totals, want)
	}

	usage := f.usageEvents()
	if len(usage) != 3 {
		t.Fatalf("token.usage events = %d, want 3", len(usage))
	}
	ids := map[string]bool{}
	var credits float64
	for _, ev := range usage {
		if ids[ev.Event.ID] {
			t.Fatalf("duplicate event id %s", ev.Event.ID)
		}
		ids[ev.Event.ID] = true
		if ev.GenAI.Usage.CostUSD != nil {
			t.Fatalf("Factory credits are not USD; cost_usd = %v", *ev.GenAI.Usage.CostUSD)
		}
		credits += ev.Raw["factory_credits_delta"].(float64)
	}
	if credits != 6 {
		t.Fatalf("summed factory_credits_delta = %v, want the final total 6", credits)
	}
	last := usage[len(usage)-1]
	if got := last.Raw["factory_credits_total"]; got != 6.0 {
		t.Fatalf("factory_credits_total = %v, want 6", got)
	}
	if got := *last.GenAI.Usage.InputTokens; got != 150 {
		t.Fatalf("last delta input = %d, want 150", got)
	}
}

// A field that goes down is a rewritten settings file, not negative spend. It emits nothing for
// that field and the lower value becomes the baseline, so growth after the rewrite still counts.
func TestCollectOnceFactoryUsageDecreaseRebases(t *testing.T) {
	f := newUsageFixture(t)
	f.writeSettings(`{"tokenUsage":{"inputTokens":100,"outputTokens":50}}`)
	f.sweep()
	f.writeSettings(`{"tokenUsage":{"inputTokens":40,"outputTokens":60}}`)
	f.sweep()
	f.writeSettings(`{"tokenUsage":{"inputTokens":70,"outputTokens":60}}`)
	f.sweep()

	report := tokens.Aggregate(f.events(), tokens.Options{})
	if report.Totals.InputTokens != 130 || report.Totals.OutputTokens != 60 {
		t.Fatalf("totals = %+v, want input 100+0+30=130 and output 50+10=60", report.Totals)
	}
	for _, ev := range f.usageEvents() {
		u := ev.GenAI.Usage
		if (u.InputTokens != nil && *u.InputTokens < 0) || (u.OutputTokens != nil && *u.OutputTokens < 0) {
			t.Fatalf("negative delta emitted: %+v", u)
		}
	}
}

// inclusiveTokenUsage adds the usage of the session's child (subagent) sessions, which Factory
// stores as sessions of their own and Beacon collects separately. Counting it on the parent would
// count every child twice, so it is used only when it cannot include a child.
func TestCollectOnceFactoryInclusiveUsageNeverDoubleCountsChildren(t *testing.T) {
	f := newUsageFixture(t)
	// Older settings without tokenUsage and no children: inclusive is the session's own usage.
	f.writeSettings(`{"inclusiveTokenUsage":{"inputTokens":100,"outputTokens":10}}`)
	f.sweep()
	// tokenUsage appears alongside an inclusive total that now counts a child session.
	f.writeSettings(`{"tokenUsage":{"inputTokens":150,"outputTokens":20},"inclusiveTokenUsage":{"inputTokens":900,"outputTokens":90},"childInclusiveTokenUsageBySessionId":{"child-1":{"inputTokens":750,"outputTokens":70}}}`)
	f.sweep()
	// Only the inclusive total is present while a child is: nothing of the parent's own usage
	// can be separated out, so nothing is emitted and the baseline stays.
	f.writeSettings(`{"inclusiveTokenUsage":{"inputTokens":1000,"outputTokens":100},"childInclusiveTokenUsageBySessionId":{"child-1":{"inputTokens":800,"outputTokens":75}}}`)
	if summary := f.sweep(); summary.EventsEmitted != 0 {
		t.Fatalf("inclusive-with-children sweep emitted %d events, want 0", summary.EventsEmitted)
	}
	f.writeSettings(`{"tokenUsage":{"inputTokens":200,"outputTokens":25},"inclusiveTokenUsage":{"inputTokens":1000,"outputTokens":100},"childInclusiveTokenUsageBySessionId":{"child-1":{"inputTokens":800,"outputTokens":75}}}`)
	f.sweep()

	report := tokens.Aggregate(f.events(), tokens.Options{})
	if report.Totals.InputTokens != 200 || report.Totals.OutputTokens != 25 {
		t.Fatalf("totals = %+v, want the session's own final usage 200/25", report.Totals)
	}
}

// State written before Beacon tracked the totals already holds a token.usage event for the
// settings it last saw. Upgrading must not count that total a second time.
func TestCollectOnceFactoryUsageUpgradeSeedsBaselineFromUnchangedSettings(t *testing.T) {
	f := newUsageFixture(t)
	f.writeSettings(`{"tokenUsage":{"inputTokens":100,"outputTokens":10}}`)
	f.sweep()

	state, err := LoadState(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	// What the previous release stored: the settings mtime, no totals.
	state.Sessions["session-1"].Usage = nil
	if err := state.Save(f.statePath); err != nil {
		t.Fatal(err)
	}
	if summary := f.sweep(); summary.EventsEmitted != 0 {
		t.Fatalf("upgrade sweep emitted %d events, want 0", summary.EventsEmitted)
	}
	f.writeSettings(`{"tokenUsage":{"inputTokens":160,"outputTokens":12}}`)
	f.sweep()

	report := tokens.Aggregate(f.events(), tokens.Options{})
	if report.Totals.InputTokens != 160 || report.Totals.OutputTokens != 12 {
		t.Fatalf("totals = %+v, want 160/12", report.Totals)
	}
}

// The collector re-reads a session whose file moved (a renamed working directory) from the top,
// but its settings totals are the same running figure, so they are not counted again.
func TestCollectOnceFactoryUsageSurvivesAMovedSession(t *testing.T) {
	f := newUsageFixture(t)
	f.writeSettings(`{"tokenUsage":{"inputTokens":100,"outputTokens":10}}`)
	f.sweep()

	moved := filepath.Join(f.root, "-repo2")
	if err := os.Rename(filepath.Dir(f.sessionPath), moved); err != nil {
		t.Fatal(err)
	}
	f.sessionPath = filepath.Join(moved, "session-1.jsonl")
	f.settingsPath = filepath.Join(moved, "session-1.settings.json")
	f.sweep()
	f.writeSettings(`{"tokenUsage":{"inputTokens":130,"outputTokens":10}}`)
	f.sweep()

	report := tokens.Aggregate(f.events(), tokens.Options{})
	if report.Totals.InputTokens != 130 || report.Totals.OutputTokens != 10 {
		t.Fatalf("totals = %+v, want 130/10", report.Totals)
	}
}

// A write that fails leaves the totals uncommitted, so the retry emits the same delta again
// rather than losing it.
func TestCollectOnceFactoryUsageRetriesAfterFailedWrite(t *testing.T) {
	f := newUsageFixture(t)
	f.writeSettings(`{"tokenUsage":{"inputTokens":100,"outputTokens":10}}`)
	f.sweep()
	f.writeSettings(`{"tokenUsage":{"inputTokens":180,"outputTokens":15}}`)

	original := emitEvent
	emitEvent = func(event schema.Event, opts CollectOptions) error {
		if event.Event.Action == "token.usage" {
			return os.ErrPermission
		}
		return original(event, opts)
	}
	_, err := CollectOnce(CollectOptions{SessionsDir: f.root, StatePath: f.statePath, Print: true, Out: &f.out})
	emitEvent = original
	if err == nil {
		t.Fatal("expected the failed write to surface")
	}
	f.sweep()

	report := tokens.Aggregate(f.events(), tokens.Options{})
	if report.Totals.InputTokens != 180 || report.Totals.OutputTokens != 15 {
		t.Fatalf("totals = %+v, want 180/15", report.Totals)
	}
}
