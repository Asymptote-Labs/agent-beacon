package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/spf13/cobra"
)

// stubBackfill replaces the collectors with runners that each try to write events events, so a
// test sees how the orchestrator shares the budget without any real session store.
func stubBackfill(t *testing.T, maxBytes, shareBytes int64, runners ...endpointBackfillRunner) {
	t.Helper()
	t.Setenv(endpointBackfillEnv, "")
	oldRunners, oldMax, oldShare, oldNow := endpointBackfillRunners, endpointBackfillMaxBytes, endpointBackfillShareBytes, endpointBackfillNow
	t.Cleanup(func() {
		endpointBackfillRunners, endpointBackfillMaxBytes, endpointBackfillShareBytes, endpointBackfillNow = oldRunners, oldMax, oldShare, oldNow
	})
	endpointBackfillRunners = func() []endpointBackfillRunner { return runners }
	endpointBackfillMaxBytes, endpointBackfillShareBytes = maxBytes, shareBytes
	endpointBackfillNow = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	oldScratch := endpointBackfillScratchDir
	t.Cleanup(func() { endpointBackfillScratchDir = oldScratch })
	scratch := filepath.Join(t.TempDir(), "backfill-snapshots")
	endpointBackfillScratchDir = func() string { return scratch }
}

func writingRunner(name string, events int, since *time.Time) endpointBackfillRunner {
	return endpointBackfillRunner{Name: name, Sync: "beacon endpoint " + strings.ToLower(name) + " sync", Run: func(r endpointBackfillRun) (int, error) {
		if since != nil {
			*since = r.Since
		}
		written := 0
		for i := 0; i < events; i++ {
			event := schema.NewEvent(schema.NewEventOptions{
				Action:   "prompt.submitted",
				Category: "agent",
				Severity: schema.SeverityInfo,
				Harness:  schema.HarnessInfo{Name: strings.ToLower(name), CollectionMethod: schema.CollectionMethodPoll},
				Message:  strings.Repeat("x", 200) + name + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			})
			if _, err := writer.AppendEvent(event, writer.Options{Path: r.LogPath, UserMode: true, Budget: r.Budget}); err != nil {
				return written, errors.Join(err)
			}
			written++
		}
		return written, nil
	}}
}

func TestEndpointBackfillGivesEachRuntimeAShareAndNamesWhereToContinue(t *testing.T) {
	var since time.Time
	stubBackfill(t, 6000, 2500,
		writingRunner("Heavy", 100, &since),
		writingRunner("Light", 2, nil),
		writingRunner("Late", 100, nil),
		endpointBackfillRunner{Name: "Broken", Sync: "beacon endpoint broken sync", Run: func(endpointBackfillRun) (int, error) {
			return 0, errors.New("store unreadable")
		}},
		writingRunner("Absent", 0, nil),
	)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	result := runEndpointBackfill(logPath, true)

	if want := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC); !since.Equal(want) {
		t.Fatalf("collectors were given since=%s, want %s", since, want)
	}
	byName := map[string]endpointBackfillRuntimeResult{}
	for _, r := range result.Runtimes {
		byName[r.Name] = r
	}
	if _, ok := byName["Absent"]; ok {
		t.Fatal("a runtime with nothing to read is reported")
	}
	if !byName["Heavy"].Limited || byName["Light"].Limited || byName["Light"].Events != 2 || !byName["Late"].Limited {
		t.Fatalf("runtimes = %+v", result.Runtimes)
	}
	if byName["Late"].Events == 0 {
		t.Fatal("the first runtime's share left nothing for the ones after it")
	}
	if byName["Broken"].Err == nil || byName["Heavy"].Err != nil {
		t.Fatalf("errors: heavy=%v broken=%v", byName["Heavy"].Err, byName["Broken"].Err)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 6000 || result.Bytes != info.Size() || !result.Limited {
		t.Fatalf("log holds %d bytes, result=%+v", info.Size(), result)
	}

	var out strings.Builder
	reportEndpointBackfill(&out, result)
	text := out.String()
	for _, want := range []string{"`beacon endpoint heavy sync`", "`beacon endpoint broken sync`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}

// A collector that stops before rotation would discard its own output has left the rest for its
// sync, exactly as one that spent its share has: limited, not failed, and named as such.
func TestEndpointBackfillReportsARetentionStopAsLimited(t *testing.T) {
	stubBackfill(t, 1<<20, 1<<20,
		endpointBackfillRunner{Name: "Rotating", Sync: "beacon endpoint rotating sync", Run: func(endpointBackfillRun) (int, error) {
			return 7, errors.Join(writer.ErrRetentionWindowFull)
		}},
		endpointBackfillRunner{Name: "Budget", Sync: "beacon endpoint budget sync", Run: func(endpointBackfillRun) (int, error) {
			return 3, writer.ErrBudgetSpent
		}},
	)
	result := runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), true)
	byName := map[string]endpointBackfillRuntimeResult{}
	for _, r := range result.Runtimes {
		byName[r.Name] = r
	}
	rotating, budget := byName["Rotating"], byName["Budget"]
	if !result.Limited || !rotating.Limited || !rotating.RetentionLimited || rotating.Err != nil || rotating.Events != 7 {
		t.Fatalf("retention stop = %+v, result limited %v", rotating, result.Limited)
	}
	if !budget.Limited || budget.RetentionLimited || budget.Err != nil {
		t.Fatalf("budget stop = %+v", budget)
	}
}

func TestEndpointInstallBackfillsAfterTheInstallSucceeds(t *testing.T) {
	recordEndpointLifecycle(t)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointLifecycleInstall = func(lifecycle.InstallOptions) (lifecycle.InstallResult, error) {
		return lifecycle.InstallResult{LogPath: logPath}, nil
	}
	stubBackfill(t, 1<<20, 1<<20, writingRunner("Codex", 3, nil))

	run := func(mutate func()) {
		t.Helper()
		_ = os.Remove(logPath)
		endpointOpts.harnesses = ""
		endpointOpts.noBackfill = false
		endpointOpts.userMode, endpointOpts.systemMode = true, false
		mutate()
		var out strings.Builder
		cmd := &cobra.Command{Use: "install"}
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := runEndpointInstall(cmd, nil); err != nil {
			t.Fatal(err)
		}
	}

	run(func() {})
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("install did not write the backfill to the runtime log: %v", err)
	}
	for name, mutate := range map[string]func(){
		"--no-backfill":     func() { endpointOpts.noBackfill = true },
		"BEACON_BACKFILL=0": func() { t.Setenv(endpointBackfillEnv, "0") },
	} {
		run(mutate)
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("%s still wrote the runtime log", name)
		}
		t.Setenv(endpointBackfillEnv, "")
	}
}

func TestSystemEndpointBackfillReadsTheConsoleUsersStores(t *testing.T) {
	home := t.TempDir()
	var sawHome string
	var sawUserMode bool
	stubBackfill(t, 1<<20, 1<<20, endpointBackfillRunner{Name: "Codex", Sync: "beacon endpoint codex sync", Run: func(r endpointBackfillRun) (int, error) {
		sawHome, _ = os.UserHomeDir()
		sawUserMode = r.UserMode
		return writingRunner("Codex", 2, nil).Run(r)
	}})
	stubConsoleUser(t, consoleUserInfo{Username: "alice", HomeDir: home}, true, nil)
	before, _ := os.UserHomeDir()

	runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), false)
	if sawHome != home || sawUserMode {
		t.Fatalf("system backfill read home=%q userMode=%t, want the console user's home %q in system mode", sawHome, sawUserMode, home)
	}
	if after, _ := os.UserHomeDir(); after != before {
		t.Fatalf("system backfill left HOME at %q, want %q", after, before)
	}

	stubConsoleUser(t, consoleUserInfo{}, false, nil)
	sawHome = ""
	runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), false)
	if sawHome != "" {
		t.Fatal("a system backfill with no console user read root's home")
	}
}

func TestConnectShipsTheBackfill(t *testing.T) {
	originalLoad, originalConnect := connectAccountLoad, connectManagedEndpoint
	originalConnectOpts, originalEndpointOpts := connectOpts, endpointOpts
	t.Cleanup(func() {
		connectAccountLoad, connectManagedEndpoint = originalLoad, originalConnect
		connectOpts, endpointOpts = originalConnectOpts, originalEndpointOpts
	})
	stubBackfill(t, 1<<20, 1<<20, writingRunner("Codex", 2, nil))
	connectOpts.privacyMode = "standard"
	connectAccountLoad = func() (*account.Session, error) {
		return &account.Session{
			BaseURL: "https://beacon.sh", AccessToken: "bcn_cli_secret", ExpiresAt: time.Now().Add(time.Hour),
			Scopes: []string{account.ScopeProfileRead, account.ScopeDeviceEnroll},
		}, nil
	}
	var captured asymptote.ConnectOptions
	staged := 0
	connectManagedEndpoint = func(_ context.Context, options asymptote.ConnectOptions) (*asymptote.ConnectResult, error) {
		captured = options
		result := &asymptote.ConnectResult{
			Enrollment:     asymptote.Enrollment{DeviceID: "dev-1", DashboardURL: "https://beacon.sh"},
			ForwarderState: service.Status{Loaded: true, Running: true},
		}
		if options.Backfill != nil {
			result.Backfill = &asymptote.BackfillResult{Events: staged}
		}
		return result, nil
	}
	connect := func(backfilled bool) {
		t.Helper()
		var out strings.Builder
		command := &cobra.Command{}
		command.SetOut(&out)
		command.SetErr(&out)
		if err := connectEndpoint(command, true, filepath.Join(t.TempDir(), "runtime.jsonl"), backfilled); err != nil {
			t.Fatal(err)
		}
	}

	staged = 2
	connect(false)
	if captured.Backfill == nil || captured.Backfill.Sync == nil || captured.Backfill.Options.MaxBytes != endpointBackfillMaxBytes ||
		!captured.Backfill.Options.Since.Equal(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("connect backfill = %+v", captured.Backfill)
	}

	connect(true)
	if captured.Backfill == nil || captured.Backfill.Sync != nil {
		t.Fatalf("connect after an install backfill swept again: %+v", captured.Backfill)
	}

	t.Setenv(endpointBackfillEnv, "0")
	staged = 0
	if connect(false); captured.Backfill != nil {
		t.Fatalf("BEACON_BACKFILL=0 still shipped a backfill: %+v", captured.Backfill)
	}
}
