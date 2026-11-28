package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/osuser"
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

// Without root, repairing the current user's own hooks (user-mode enforcement) runs the child
// directly: sudo would only ask for a password to become who it already is.
func TestConsoleUserCommandRunsDirectlyForTheCurrentUserWithoutRoot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX, without root")
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	cmd := consoleUserCommand(context.Background(), consoleUserInfo{Username: me.Username, HomeDir: me.HomeDir}, "/u/beacon", "endpoint", "hooks", "install")
	if got := strings.Join(cmd.Args, " "); got != "/u/beacon endpoint hooks install" {
		t.Fatalf("args = %q, want the child run directly", got)
	}
	if !slices.Contains(cmd.Env, "BEACON_INTERNAL_CHILD=1") {
		t.Fatal("the child is not marked internal")
	}
}

func TestRepairInstalledEndpointUserConfigConfiguresNamedAccounts(t *testing.T) {
	writeNativeConfigInProcess(t)
	oldOpts, oldUsers, oldLookup := endpointOpts, activeConsoleUsers, lookupConsoleUser
	oldLookupOSAccount := lookupOSAccount
	oldRunHooks, oldLoadConfig := runHookRepairAsUser, loadConfigForUserRepair
	t.Cleanup(func() {
		endpointOpts, activeConsoleUsers, lookupConsoleUser = oldOpts, oldUsers, oldLookup
		lookupOSAccount = oldLookupOSAccount
		runHookRepairAsUser, loadConfigForUserRepair = oldRunHooks, oldLoadConfig
	})

	toorHome := t.TempDir()
	tests := []struct {
		name          string
		accounts      []string
		realLookup    bool
		lookupAccount func(string) (osuser.Info, error)
		wantUsers     string
		wantErr       string
	}{
		{name: "single account with no login", accounts: []string{"dave"}, wantUsers: "dave"},
		{name: "duplicate names configured once in order", accounts: []string{"dave", "erin", "dave"}, wantUsers: "dave,erin"},
		{name: "unresolvable name rejects all", accounts: []string{"dave", "nobody"}, wantErr: `"nobody"`},
		{name: "root rejected by real resolver", accounts: []string{"root"}, realLookup: true, wantErr: `"root"`},
		{name: "uid 0 alias rejected", accounts: []string{"toor"}, realLookup: true, lookupAccount: func(name string) (osuser.Info, error) {
			return osuser.Info{Username: "toor", UID: 0, GID: 0, HomeDir: toorHome}, nil
		}, wantErr: `"toor"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homes := map[string]string{"dave": t.TempDir(), "erin": t.TempDir()}
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			endpointOpts = oldOpts
			endpointOpts.userMode, endpointOpts.systemMode = true, true
			endpointOpts.logPath, endpointOpts.hookHarnesses, endpointOpts.hookLevel = logPath, "", "user"
			endpointOpts.configAccounts = append([]string(nil), tt.accounts...)
			lookupOSAccount = oldLookupOSAccount
			if tt.lookupAccount != nil {
				lookupOSAccount = tt.lookupAccount
			}
			activeConsoleUsers = func() ([]consoleUserInfo, error) {
				t.Fatal("activeConsoleUsers was consulted for explicit --account values")
				return nil, nil
			}
			if tt.realLookup {
				lookupConsoleUser = resolveConsoleUser
			} else {
				lookupConsoleUser = func(name string) (consoleUserInfo, bool, error) {
					home, ok := homes[name]
					return consoleUserInfo{Username: name, HomeDir: home}, ok, nil
				}
			}
			loadConfigForUserRepair = func(userMode bool, logPath string) endpointconfig.Config {
				return endpointconfig.Default(userMode, logPath)
			}
			var hooked []string
			var hookArgs []string
			runHookRepairAsUser = func(info consoleUserInfo, args ...string) error {
				hooked = append(hooked, info.Username)
				if info.Username == "dave" {
					hookArgs = append([]string(nil), args...)
				}
				return nil
			}

			result, err := repairInstalledEndpointUserConfig()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to name %s", err, tt.wantErr)
				}
				if result.Error != err.Error() {
					t.Fatalf("result error = %q, want joined error %q", result.Error, err)
				}
				if result.SkippedReason != "" || len(result.Users) != 0 {
					t.Fatalf("result = %+v, want no skip reason and no configured users", result)
				}
				if len(hooked) != 0 {
					t.Fatalf("hook repair ran for %v before validation completed", hooked)
				}
				if _, err := os.Stat(filepath.Join(homes["dave"], ".claude", "settings.json")); !os.IsNotExist(err) {
					t.Fatalf("dave's settings were written before validation completed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var users []string
			for _, configured := range result.Users {
				users = append(users, configured.User)
			}
			if got := strings.Join(users, ","); got != tt.wantUsers {
				t.Fatalf("users = %q, want %q", got, tt.wantUsers)
			}
			if got := strings.Join(hooked, ","); got != tt.wantUsers {
				t.Fatalf("hook repair order = %q, want %q", got, tt.wantUsers)
			}
			if tt.wantUsers == "dave" {
				args := strings.Join(hookArgs, " ")
				if !strings.Contains(args, "--level user") || !strings.Contains(args, "--log-path "+logPath) {
					t.Fatalf("hook args = %q, want user level and the system log path", args)
				}
				settings, err := os.ReadFile(filepath.Join(homes["dave"], ".claude", "settings.json"))
				if err != nil || !strings.Contains(string(settings), "127.0.0.1:4317") {
					t.Fatalf("dave's Claude settings = %q, %v; want the local collector", settings, err)
				}
			}
		})
	}
}

func TestRepairInstalledEndpointUserConfigAccountFlag(t *testing.T) {
	accountFlag := endpointUserConfigRepairInstalledCmd.Flags().Lookup("account")
	userFlag := endpointUserConfigRepairInstalledCmd.Flags().Lookup("user")
	if accountFlag == nil || userFlag == nil {
		t.Fatal("repair-installed must have --account and --user flags")
	}
	if got := accountFlag.Value.Type(); got != "stringArray" {
		t.Fatalf("--account type = %q, want stringArray", got)
	}
	if got := userFlag.Value.Type(); got != "bool" {
		t.Fatalf("--user type = %q, want bool", got)
	}
}
