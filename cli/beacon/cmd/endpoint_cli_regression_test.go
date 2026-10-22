package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// runBeaconCLI runs `beacon <args>` through the root command the way a fresh process would: the
// leaf command's flags start at their registered defaults, because this CLI keeps flag values in
// package variables that earlier tests leave set. Stdout is captured whether the command writes
// to cmd.OutOrStdout or straight to os.Stdout. The flags and endpointOpts are restored afterwards.
func runBeaconCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	leaf, _, err := rootCmd.Find(args)
	if err != nil {
		t.Fatalf("beacon %s: %v", strings.Join(args, " "), err)
	}
	reset := func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	oldOpts := endpointOpts
	t.Cleanup(func() { endpointOpts = oldOpts })
	t.Cleanup(func() { reset(leaf) })
	reset(leaf)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	stdout, err := captureStdout(t, rootCmd.Execute)
	return out.String() + stdout, err
}

// Without --user or --system, the endpoint commands work on the per-user install: the person
// running `beacon endpoint install` from a shell gets the collector, config and log under their
// own home, never the root-owned system paths. Run as the CLI so the flag defaults are what count.
func TestEndpointCommandsRunInUserModeWithoutFlags(t *testing.T) {
	calls := recordEndpointLifecycle(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"endpoint", "install", "--harness="},
		{"endpoint", "repair", "--harness="},
	} {
		*calls = nil
		if out, err := runBeaconCLI(t, args...); err != nil {
			t.Fatalf("beacon %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if len(*calls) != 1 || !(*calls)[0].UserMode {
			t.Fatalf("beacon %s asked the lifecycle for %+v, want one user-mode install", strings.Join(args, " "), *calls)
		}
	}

	out, err := runBeaconCLI(t, "endpoint", "config", "show")
	if err != nil {
		t.Fatalf("config show: %v\n%s", err, out)
	}
	var cfg struct {
		UserMode bool   `json:"user_mode"`
		LogPath  string `json:"log_path"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("config show printed %q: %v", out, err)
	}
	if rel, err := filepath.Rel(home, cfg.LogPath); !cfg.UserMode || err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("config show = %+v, want user mode with the log under %s", cfg, home)
	}
}

// Regression for #619: a backfill of a large history uses --rotate-bytes so the sweep does not
// rotate the runtime log out from under itself. The flag has to reach the writer: with a log one
// write short of the default rotation size, a sync at the default rotates it, and a sync with a
// larger --rotate-bytes appends to it in place.
func TestSessionSyncRotateBytesReachesTheRuntimeLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(t *testing.T, root string) []string
	}{
		{"claude", func(t *testing.T, root string) []string {
			projects := filepath.Join(root, "projects")
			session := filepath.Join(projects, "-tmp-repo", "sess-1.jsonl")
			if err := os.MkdirAll(filepath.Dir(session), 0o755); err != nil {
				t.Fatal(err)
			}
			line := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hello"},"uuid":"u1","timestamp":"2026-09-19T22:00:00.000Z","cwd":"/tmp/repo","sessionId":"sess-1","version":"2.1.154","gitBranch":"main"}`
			if err := os.WriteFile(session, []byte(line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"endpoint", "claude", "sync", "--projects-dir", projects}
		}},
		{"devin", func(t *testing.T, root string) []string {
			db := filepath.Join(root, "sessions.db")
			writeDevinFixtureDB(t, db)
			return []string{"endpoint", "devin", "sync", "--db", db}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.SetHome(t, t.TempDir())
			sync := func(extra ...string) (logPath string) {
				t.Helper()
				root := t.TempDir()
				logPath = filepath.Join(root, "logs", "runtime.jsonl")
				fillRuntimeLogNearRotation(t, logPath)
				args := append(tc.fixture(t, root), "--state", filepath.Join(root, "state.json"), "--log-path", logPath)
				if out, err := runBeaconCLI(t, append(args, extra...)...); err != nil {
					t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
				}
				return logPath
			}

			if logPath := sync(); !fileExists(logPath + ".1") {
				t.Fatal("the fixture does not reach the default rotation size, so it proves nothing")
			}
			logPath := sync("--rotate-bytes", strconv.FormatInt(4*writer.DefaultRotateBytes, 10))
			if fileExists(logPath + ".1") {
				t.Fatal("--rotate-bytes did not reach the writer: the log rotated at the default size")
			}
			if info, err := os.Stat(logPath); err != nil || info.Size() <= writer.DefaultRotateBytes-64 {
				t.Fatalf("the sync did not append to the runtime log: %v", err)
			}
		})
	}
}

// fillRuntimeLogNearRotation writes a runtime log 64 bytes short of the default rotation size.
func fillRuntimeLogNearRotation(t *testing.T, logPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"filler":"` + strings.Repeat("x", 1000) + `"}` + "\n"
	size := int(writer.DefaultRotateBytes - 64)
	data := []byte(strings.Repeat(line, size/len(line)+1)[:size-1] + "\n")
	if err := os.WriteFile(logPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Most machines have only one or two of the runtimes Beacon collects from. Every session sync
// (and its status command) must succeed quietly where its runtime has never been installed,
// writing no runtime log: install's backfill runs all of them, and an error from an absent store
// reads as a broken install.
func TestEverySessionSyncIsAQuietNoOpWithoutItsRuntime(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	// Each runtime's own directory override would otherwise point outside the empty home.
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME", "CURSOR_CONFIG_DIR", "CLINE_DIR", "DSH_HOME",
		"HERMES_HOME", "HERMES_DATA_DIR_SUFFIX", "OPENCLAW_STATE_DIR", "OPENCODE_DB", "PI_CODING_AGENT_DIR",
		"PRIME_AGENT_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, "")
	}
	logPath := writer.DefaultPath(true)
	for _, runner := range endpointBackfillRunners() {
		syncArgs := strings.Fields(strings.TrimPrefix(runner.Sync, "beacon "))
		t.Run(runner.Name, func(t *testing.T) {
			commands := [][]string{syncArgs}
			statusArgs := append(append([]string{}, syncArgs[:len(syncArgs)-1]...), "status")
			if status, _, err := rootCmd.Find(statusArgs); err == nil && status.Name() == "status" {
				commands = append(commands, statusArgs)
			}
			for _, args := range commands {
				if out, err := runBeaconCLI(t, args...); err != nil {
					t.Fatalf("beacon %s on a machine without %s: %v\n%s", strings.Join(args, " "), runner.Name, err, out)
				}
			}
			if fileExists(logPath) {
				t.Fatalf("beacon %s with nothing to collect created %s", runner.Sync, logPath)
			}
		})
	}
}

// `endpoint integrations validate --all` is what an operator or MDM check runs to see whether the
// admin integrations deliver; its exit status is the answer, in text mode as much as with --json.
func TestIntegrationsValidateAllExitsNonZeroWhenAnIntegrationIsBroken(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	for _, extra := range [][]string{nil, {"--json"}} {
		args := append([]string{"endpoint", "integrations", "validate", "--all", "--log-path", missing}, extra...)
		out, err := runBeaconCLI(t, args...)
		if err == nil {
			t.Fatalf("beacon %s exited 0 with nothing delivered:\n%s", strings.Join(args, " "), out)
		}
		for _, target := range []string{"claude-cowork", "openclaw"} {
			if !strings.Contains(out, target) {
				t.Fatalf("beacon %s does not report %s:\n%s", strings.Join(args, " "), target, out)
			}
		}
	}
}
