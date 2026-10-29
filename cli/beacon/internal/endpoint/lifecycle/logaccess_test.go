package lifecycle

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/loggroup"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// A repair or package upgrade re-runs install, which rebuilds config.json. A restricted log that
// came back shared would be world-writable again with nobody having asked for it.
func TestBuildConfigKeepsTheLogAccessMode(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"log_path":"/tmp/runtime.jsonl","log_access":"restricted"}`)
	if cfg := buildConfig(InstallOptions{UserMode: true}); cfg.LogAccess != "restricted" {
		t.Fatalf("LogAccess = %q, want the existing mode carried over", cfg.LogAccess)
	}
	if cfg := buildConfig(InstallOptions{UserMode: true, LogAccess: "shared"}); cfg.LogAccess != "shared" {
		t.Fatalf("LogAccess = %q, want the explicit option to win", cfg.LogAccess)
	}
}

func TestBuildConfigWithoutAnExistingConfigIsShared(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	if cfg := buildConfig(InstallOptions{UserMode: true}); cfg.LogAccess != "" || cfg.RestrictedLogAccess() {
		t.Fatalf("a fresh install has log access %q", cfg.LogAccess)
	}
}

func TestValidateLogAccess(t *testing.T) {
	system := func(mode string) endpointconfig.Config {
		return endpointconfig.Config{UserMode: false, LogAccess: mode}
	}
	for _, mode := range []string{"", "shared", "Shared"} {
		if err := validateLogAccess(system(mode)); err != nil {
			t.Errorf("validateLogAccess(%q) = %v", mode, err)
		}
	}
	if err := validateLogAccess(system("open")); err == nil {
		t.Error("accepted an unknown mode")
	}
	err := validateLogAccess(endpointconfig.Config{UserMode: true, LogAccess: "restricted"})
	if err == nil || !strings.Contains(err.Error(), "system install") {
		t.Errorf("restricted in user mode = %v, want a refusal naming the system install", err)
	}
	err = validateLogAccess(system("restricted"))
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Error("restricted was accepted on Windows")
		}
	} else if err != nil {
		t.Errorf("restricted on a system install = %v", err)
	}
}

type logAccessCalls struct {
	ensured      []string
	restricted   []int
	shared       int
	staleRemoved int
}

func fakeLogAccess(t *testing.T, gid int, created bool, ensureErr, restrictErr error) *logAccessCalls {
	t.Helper()
	calls := &logAccessCalls{}
	prevEnsure, prevRestrict, prevShare := ensureLogReadersGroup, restrictSystemLog, shareSystemLog
	t.Cleanup(func() { ensureLogReadersGroup, restrictSystemLog, shareSystemLog = prevEnsure, prevRestrict, prevShare })
	ensureLogReadersGroup = func(name string) (int, bool, error) {
		calls.ensured = append(calls.ensured, name)
		return gid, created, ensureErr
	}
	restrictSystemLog = func(path string, g int) error {
		calls.restricted = append(calls.restricted, g)
		return restrictErr
	}
	shareSystemLog = func(string) error {
		calls.shared++
		return nil
	}
	prevStale := removeStaleIngestSocket
	removeStaleIngestSocket = func(string) error { calls.staleRemoved++; return nil }
	t.Cleanup(func() { removeStaleIngestSocket = prevStale })
	return calls
}

func TestPrepareSystemLogRestricts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no restricted mode on Windows")
	}
	calls := fakeLogAccess(t, 4242, true, nil, nil)
	created, err := prepareSystemLog(endpointconfig.Config{LogPath: "/var/log/beacon-agent/runtime.jsonl", LogAccess: "restricted"})
	if err != nil || !created {
		t.Fatalf("prepareSystemLog = %v, %v", created, err)
	}
	if len(calls.ensured) != 1 || calls.ensured[0] != loggroup.Name {
		t.Fatalf("ensured groups %v", calls.ensured)
	}
	if len(calls.restricted) != 1 || calls.restricted[0] != 4242 || calls.shared != 0 {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestPrepareSystemLogShares(t *testing.T) {
	calls := fakeLogAccess(t, 0, false, nil, nil)
	if _, err := prepareSystemLog(endpointconfig.Config{LogPath: "/var/log/beacon-agent/runtime.jsonl"}); err != nil {
		t.Fatal(err)
	}
	if calls.shared != 1 || calls.staleRemoved != 1 || len(calls.ensured) != 0 || len(calls.restricted) != 0 {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestPrepareSystemLogStopsWhenTheGroupCannotBeMade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no restricted mode on Windows")
	}
	calls := fakeLogAccess(t, 0, false, errors.New("groupadd: permission denied"), nil)
	_, err := prepareSystemLog(endpointconfig.Config{LogPath: "/var/log/beacon-agent/runtime.jsonl", LogAccess: "restricted"})
	if err == nil || !strings.Contains(err.Error(), loggroup.Name) {
		t.Fatalf("err = %v", err)
	}
	if len(calls.restricted) != 0 {
		t.Fatal("narrowed the log without a group to give readers")
	}
}

func TestLogAccessOf(t *testing.T) {
	if got := logAccessOf(endpointconfig.Config{UserMode: true, LogAccess: "restricted"}); got != "" {
		t.Fatalf("user mode = %q", got)
	}
	if got := logAccessOf(endpointconfig.Config{}); got != "shared" {
		t.Fatalf("system default = %q", got)
	}
	if runtime.GOOS != "windows" {
		if got := logAccessOf(endpointconfig.Config{LogAccess: "restricted"}); got != "restricted" {
			t.Fatalf("restricted = %q", got)
		}
	}
}

func TestLogAccessRestorerPutsBackWhatAFailedInstallChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no restricted mode on Windows")
	}
	restricted := false
	prev := logIsRestricted
	logIsRestricted = func(string) bool { return restricted }
	t.Cleanup(func() { logIsRestricted = prev })
	calls := fakeLogAccess(t, 77, false, nil, nil)
	cfg := endpointconfig.Config{LogPath: "/var/log/beacon-agent/runtime.jsonl"}

	// Shared before; the failed install restricted it.
	restore := logAccessRestorer(cfg)
	restricted = true
	restore()
	if calls.shared != 1 || len(calls.restricted) != 0 {
		t.Fatalf("restoring a shared log: %+v", calls)
	}

	// Restricted before; the failed install shared it.
	restricted = true
	restore = logAccessRestorer(cfg)
	restricted = false
	restore()
	if len(calls.restricted) != 1 || calls.restricted[0] != 77 {
		t.Fatalf("restoring a restricted log: %+v", calls)
	}

	// Unchanged: nothing to do.
	restore = logAccessRestorer(cfg)
	restore()
	if calls.shared != 1 || len(calls.restricted) != 1 {
		t.Fatalf("an unchanged mode was touched: %+v", calls)
	}
}

func TestRollbackRestoresTheLogAccessMode(t *testing.T) {
	called := false
	tx := newInstallRollback(service.Manager{UserMode: true, Kind: service.KindSupervised})
	tx.RestoreLogAccess = func() { called = true }
	tx.Rollback(Manifest{})
	if !called {
		t.Fatal("Rollback did not restore the log access mode")
	}
}
