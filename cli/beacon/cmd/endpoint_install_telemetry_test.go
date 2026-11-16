package cmd

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
	"github.com/spf13/cobra"
)

type capturedEvent struct {
	name  string
	props map[string]any
}

// recordTelemetry swaps the capture seam for one that keeps every event, and turns on the sweep's
// read-back of the sessions it wrote, which is otherwise only done with telemetry on.
func recordTelemetry(t *testing.T) *[]capturedEvent {
	t.Helper()
	events := &[]capturedEvent{}
	oldCapture, oldRecords := telemetryCapture, endpointSweepRecordsSessions
	telemetryCapture = func(name string, props map[string]any) {
		*events = append(*events, capturedEvent{name: name, props: props})
	}
	endpointSweepRecordsSessions = func() bool { return true }
	t.Cleanup(func() { telemetryCapture, endpointSweepRecordsSessions = oldCapture, oldRecords })
	return events
}

func eventsNamed(events []capturedEvent, name string) []capturedEvent {
	var out []capturedEvent
	for _, e := range events {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

// sessionRunner writes perSession poll records for each of sessions under harnessName.
func sessionRunner(name, harnessName string, sessions []string, perSession int) endpointBackfillRunner {
	return endpointBackfillRunner{Name: name, Sync: "beacon endpoint " + harnessName + " sync", Run: func(r endpointBackfillRun) (int, error) {
		written := 0
		for _, id := range sessions {
			for i := 0; i < perSession; i++ {
				event := schema.NewEvent(schema.NewEventOptions{
					Action:   "prompt.submitted",
					Category: "agent",
					Severity: schema.SeverityInfo,
					Harness:  schema.HarnessInfo{Name: harnessName, CollectionMethod: schema.CollectionMethodPoll},
					Message:  "m",
				})
				event.Session = &schema.SessionInfo{ID: id}
				if _, err := writer.AppendEvent(event, writer.Options{Path: r.LogPath, UserMode: true, Budget: r.Budget}); err != nil {
					return written, err
				}
				written++
			}
		}
		return written, nil
	}}
}

func TestSessionsWrittenSinceCountsOnlyNewSessionStoreSessions(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	write := func(harnessName, method, session string) {
		event := schema.NewEvent(schema.NewEventOptions{
			Action: "prompt.submitted", Category: "agent", Severity: schema.SeverityInfo,
			Harness: schema.HarnessInfo{Name: harnessName, CollectionMethod: method},
		})
		event.Session = &schema.SessionInfo{ID: session}
		if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: true}); err != nil {
			t.Fatal(err)
		}
	}
	write("claude_code", schema.CollectionMethodPoll, "old")
	offset := logSize(logPath)
	write("claude_code", schema.CollectionMethodPoll, "s1")
	write("claude_code", schema.CollectionMethodPoll, "s1")
	write("claude_code", schema.CollectionMethodHook, "live")
	write("codex", schema.CollectionMethodPoll, "s1")
	write("codex", schema.CollectionMethodPoll, "")

	got := sessionsWrittenSince(logPath, offset)
	want := []sweptSession{{Harness: "claude_code", ID: "s1"}, {Harness: "codex", ID: "s1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %+v, want %+v", got, want)
	}
	if got := sessionsWrittenSince(logPath, logSize(logPath)+1); got != nil {
		t.Fatalf("a log shorter than the offset yielded %+v", got)
	}
	if got := sessionsWrittenSince(filepath.Join(t.TempDir(), "missing.jsonl"), 0); got != nil {
		t.Fatalf("a missing log yielded %+v", got)
	}
}

func TestInstallSetupPath(t *testing.T) {
	newOnboardingHarness(t)
	t.Setenv(installSourceEnv, "")
	for _, key := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CURSOR_AGENT", "CODEX_SANDBOX", "CODEX_THREAD_ID", "GEMINI_CLI", "OPENCODE", "CLINE_ACTIVE", "AI_AGENT"} {
		t.Setenv(key, "")
	}
	for name := range envWithPrefix("CODEX_MANAGED_BY_") {
		t.Setenv(name, "")
	}
	fresh := onboarding.Profile{}
	onboarded := onboarding.Profile{Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Destination: onboarding.DestinationLocal}}

	if got := installSetupPath(fresh); got != telemetry.SetupPathWizard {
		t.Errorf("interactive first install = %q", got)
	}
	if got := installSetupPath(onboarded); got != telemetry.SetupPathNoninteractive {
		t.Errorf("interactive install past onboarding = %q", got)
	}

	onboardingIsTTY = func() bool { return false }
	if got := installSetupPath(fresh); got != telemetry.SetupPathNoninteractive {
		t.Errorf("piped install = %q", got)
	}
	t.Setenv("CLAUDECODE", "1")
	if got := installSetupPath(fresh); got != telemetry.SetupPathAgent {
		t.Errorf("install run by Claude Code = %q", got)
	}
	t.Setenv("CI", "true")
	if got := installSetupPath(fresh); got != telemetry.SetupPathCI {
		t.Errorf("install in CI = %q", got)
	}
	t.Setenv(installSourceEnv, "package")
	if got := installSetupPath(fresh); got != telemetry.SetupPathPackage {
		t.Errorf("package install = %q", got)
	}
}

func envWithPrefix(prefix string) map[string]bool {
	names := map[string]bool{}
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, prefix) {
			names[name] = true
		}
	}
	return names
}

func TestEndpointInstallSendsStartedAndCompleted(t *testing.T) {
	recordEndpointLifecycle(t)
	h := newOnboardingHarness(t)
	events := recordTelemetry(t)
	t.Setenv(installSourceEnv, "")
	stubBackfill(t, 1<<20, 1<<19,
		sessionRunner("Claude Code", "claude_code", []string{"a", "b"}, 3),
		sessionRunner("Codex CLI", "codex", []string{"c"}, 1))
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointLifecycleInstall = func(lifecycle.InstallOptions) (lifecycle.InstallResult, error) {
		return lifecycle.InstallResult{LogPath: logPath}, nil
	}
	endpointOpts.harnesses = "claude,codex"
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationLocal
	t.Setenv(managedIngestEnvEnabled, "0")
	clock := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	oldNow := installTelemetryNow
	installTelemetryNow = func() time.Time { clock = clock.Add(1500 * time.Millisecond); return clock }
	t.Cleanup(func() { installTelemetryNow = oldNow })

	if err := runEndpointInstall(h.cmd, nil); err != nil {
		t.Fatal(err)
	}
	started := eventsNamed(*events, telemetry.EventInstallStarted)
	completed := eventsNamed(*events, telemetry.EventInstallCompleted)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("events = %+v", *events)
	}
	if started[0].props[telemetry.PropSetupPath] != telemetry.SetupPathWizard || started[0].props[telemetry.PropIsFirstForDevice] != true {
		t.Errorf("started = %+v", started[0].props)
	}
	if _, ok := started[0].props[telemetry.PropDetectedHarnesses].([]string); !ok {
		t.Errorf("detected_harnesses = %#v", started[0].props[telemetry.PropDetectedHarnesses])
	}
	want := map[string]any{
		telemetry.PropDestination:          telemetry.DestinationLocal,
		telemetry.PropHarnessesConfigured:  []string{"claude", "codex"},
		telemetry.PropBackfillSessionCount: 3,
		telemetry.PropDurationMS:           int64(1500),
		telemetry.PropIsFirstForDevice:     true,
	}
	if !reflect.DeepEqual(completed[0].props, want) {
		t.Errorf("completed = %+v, want %+v", completed[0].props, want)
	}
}

func TestEndpointInstallSendsNothingOnADryRunOrAFailedInstall(t *testing.T) {
	recordEndpointLifecycle(t)
	events := recordTelemetry(t)
	endpointOpts.harnesses = "claude"
	endpointOpts.dryRun = true
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if _, err := captureStdout(t, func() error { return runEndpointInstall(cmd, nil) }); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatalf("a dry run sent %+v", *events)
	}

	endpointOpts.dryRun = false
	endpointLifecycleInstall = func(lifecycle.InstallOptions) (lifecycle.InstallResult, error) {
		return lifecycle.InstallResult{}, io.ErrUnexpectedEOF
	}
	if err := runEndpointInstall(cmd, nil); err == nil {
		t.Fatal("a failed install returned no error")
	}
	if len(eventsNamed(*events, telemetry.EventInstallStarted)) != 1 || len(eventsNamed(*events, telemetry.EventInstallCompleted)) != 0 {
		t.Fatalf("a failed install sent %+v", *events)
	}
}

func TestDetectedAndConfiguredNames(t *testing.T) {
	got := detectedHarnessNames([]harness.Harness{{Name: "codex", Detected: true}, {Name: "cursor"}, {Name: "claude_code", Detected: true}})
	if !reflect.DeepEqual(got, []string{"claude_code", "codex"}) {
		t.Errorf("detected = %v", got)
	}
	if got := detectedHarnessNames(nil); got == nil || len(got) != 0 {
		t.Errorf("no detection = %#v, want an empty list", got)
	}
	got = configuredTargetNames(endpointTargetSelection{OTLP: []string{"claude", "gemini"}, Hooks: []string{"claude", "cursor"}})
	if !reflect.DeepEqual(got, []string{"claude", "cursor", "gemini"}) {
		t.Errorf("configured = %v", got)
	}
}
