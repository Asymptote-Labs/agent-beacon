package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// As root, native config repair must not write into the console user's home itself: a symlink the
// user planted there (~/.claude -> /etc) would turn root's writes, backups and chown into theirs.
// It hands the work to a child running as the user, and this process touches nothing.
func TestRepairNativeRuntimeConfigRunsAsConsoleUserWhenRoot(t *testing.T) {
	oldWrite, oldRun := writeAsConsoleUser, runNativeConfigAsUser
	t.Cleanup(func() { writeAsConsoleUser, runNativeConfigAsUser = oldWrite, oldRun })

	home := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "settings.json")
	if err := os.WriteFile(target, []byte("not yours"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	writeAsConsoleUser = func() bool { return true }
	var gotInfo consoleUserInfo
	var gotTargets []string
	var gotOpts nativeConfigOptions
	runNativeConfigAsUser = func(info consoleUserInfo, targets []string, opts nativeConfigOptions) (nativeConfigResult, error) {
		gotInfo, gotTargets, gotOpts = info, append([]string(nil), targets...), opts
		return nativeConfigResult{Targets: []string{"claude"}, Paths: []string{filepath.Join(home, ".claude", "settings.json")}}, nil
	}

	cfg := endpointconfig.Default(false, "")
	cfg.Collector.GRPCPort = 4999
	cfg.ClaudeCaptureModelContext = true
	info := consoleUserInfo{Username: "alice", HomeDir: home}
	configured, paths, err := repairNativeRuntimeConfigForUser(info, cfg, []string{"claude", "codex"})
	if err != nil {
		t.Fatalf("repairNativeRuntimeConfigForUser: %v", err)
	}
	if gotInfo != info || strings.Join(gotTargets, ",") != "claude,codex" || gotOpts != (nativeConfigOptions{GRPCPort: 4999, CaptureModelContext: true}) {
		t.Fatalf("console-user run = %#v %v %#v, want alice's home, both targets and the config's port and capture setting", gotInfo, gotTargets, gotOpts)
	}
	if strings.Join(configured, ",") != "claude" || len(paths) != 1 {
		t.Fatalf("result = %v %v, want what the console-user run reported", configured, paths)
	}

	// Nothing was written through the planted link or anywhere else in the home.
	if data, err := os.ReadFile(target); err != nil || string(data) != "not yours" {
		t.Fatalf("file behind the planted symlink = %q, %v; want it untouched", data, err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("files behind the planted symlink = %d, want only the original", len(entries))
	}
	if entries, _ := os.ReadDir(home); len(entries) != 1 {
		t.Fatalf("home entries = %d, want only the planted link", len(entries))
	}
}

// apply-native is the child side: it writes the native configs into the home of whoever runs it and
// reports what it wrote.
func TestApplyNativeWritesTheCallersHomeAndReportsPaths(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	old := applyNativeOpts
	t.Cleanup(func() { applyNativeOpts = old })
	applyNativeOpts.harnesses = "claude,codex,gemini,claude"
	applyNativeOpts.grpcPort = 4999

	var out bytes.Buffer
	cmd := endpointUserConfigApplyNativeCmd
	cmd.SetOut(&out)
	t.Cleanup(func() { cmd.SetOut(nil) })
	if err := runEndpointUserConfigApplyNative(cmd, nil); err != nil {
		t.Fatalf("apply-native: %v", err)
	}
	var result nativeConfigResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("apply-native output %q is not a result: %v", out.String(), err)
	}
	if got := strings.Join(result.Targets, ","); got != "claude,codex,gemini" {
		t.Fatalf("targets = %q, want each once", got)
	}
	for _, path := range result.Paths {
		if !strings.HasPrefix(path, home) {
			t.Fatalf("path %s is outside the caller's home %s", path, home)
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "127.0.0.1:4999") {
			t.Fatalf("%s = %q, %v; want it pointed at the collector port", path, data, err)
		}
	}

	applyNativeOpts.grpcPort = 0
	if err := runEndpointUserConfigApplyNative(cmd, nil); err == nil {
		t.Fatal("apply-native accepted port 0")
	}
}

// The launcher drops to the console user and gives the child that user's HOME, so the child's
// writes land in the right profile with the user's own permissions.
func TestConsoleUserCommandRunsAsTheUserWithTheirHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher")
	}
	cmd := consoleUserCommand(context.Background(), consoleUserInfo{Username: "alice", HomeDir: "/home/alice"}, "/opt/beacon/bin/beacon", "endpoint", "user-config", "apply-native")
	args := strings.Join(cmd.Args[1:], " ")
	if !strings.HasPrefix(args, "-u alice ") {
		t.Fatalf("launcher args = %q, want it to run as alice", args)
	}
	// TestMain sets BEACON_TELEMETRY=0; sudo and runuser drop it, so it is passed on by hand. The
	// child is marked internal, so it never uses up the first-run notice in a buffer.
	if !strings.HasSuffix(args, "env HOME=/home/alice USER=alice LOGNAME=alice BEACON_TELEMETRY=0 BEACON_INTERNAL_CHILD=1 /opt/beacon/bin/beacon endpoint user-config apply-native") {
		t.Fatalf("launcher args = %q, want alice's HOME, the telemetry opt-out, the internal marker and the child command", args)
	}
}
