package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// A user-mode Asymptote forwarder on Windows is a supervised process recorded beside the
// collector's pidfile; it never touches the SCM, and the system forwarder is unchanged.
func TestWindowsUserForwarderIsSupervised(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	user := ForwarderManager{Kind: KindWindowsService, UserMode: true}
	system := ForwarderManager{Kind: KindWindowsService}

	if got := user.Label(); got != "asymptote-forwarder" {
		t.Fatalf("label = %q", got)
	}
	path, err := user.UnitPath()
	if want := filepath.Join(endpointconfig.BaseDir(true), "asymptote-forwarder.pid"); err != nil || path != want {
		t.Fatalf("unit path = %q (%v), want %q", path, err, want)
	}
	if user.RestoresRegistration() {
		t.Fatal("a supervised forwarder has no registration to restore")
	}
	if got, _ := system.UnitPath(); got != `HKLM\SYSTEM\CurrentControlSet\Services\`+ForwarderWindowsService {
		t.Fatalf("system unit path = %q", got)
	}
	// Another forwarder without a supervised name keeps refusing user mode on Windows.
	sentinelLike := ForwarderManager{Kind: KindWindowsService, UserMode: true, Spec: ForwarderSpec{WindowsService: "BeaconTestForwarder", Label: "l"}}
	if sentinelLike.Supported() || !strings.Contains(sentinelLike.UnsupportedReason(), "--system") && runtime.GOOS == "windows" {
		t.Fatalf("a forwarder without SupervisedName: supported=%t reason=%q", sentinelLike.Supported(), sentinelLike.UnsupportedReason())
	}
	if runtime.GOOS == "windows" {
		if !user.Supported() || !user.StartsWhenStopped() {
			t.Fatalf("user forwarder on Windows: supported=%t reason=%q", user.Supported(), user.UnsupportedReason())
		}
		if st := user.Status(); st.Loaded || st.Running || st.Message != "forwarder not installed" {
			t.Fatalf("status before connect = %+v", st)
		}
		return
	}
	if user.Supported() || user.StartsWhenStopped() || !strings.Contains(user.UnsupportedReason(), "only on Windows") {
		t.Fatalf("off Windows: supported=%t reason=%q", user.Supported(), user.UnsupportedReason())
	}
	if err := user.Unload(); err != nil {
		t.Fatalf("Unload off Windows must be a no-op: %v", err)
	}
}

// The supervised mechanics, exercised with a script standing in for Vector: the record, a start
// that stays up, a restart, a stop that keeps the record, and removal.
func TestSupervisedForwarderStartsRestartsAndStops(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	testenv.SetHome(t, t.TempDir())
	fastSupervisedStop(t)
	old := supervisedForwarderStartCheck
	supervisedForwarderStartCheck = 200 * time.Millisecond
	t.Cleanup(func() { supervisedForwarderStartCheck = old })

	vector := filepath.Join(t.TempDir(), "vector")
	if err := os.WriteFile(vector, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := ForwarderManager{Kind: KindWindowsService, UserMode: true}
	config := filepath.Join(t.TempDir(), "vector.toml")
	if _, err := m.writeSupervisedUnit(vector, config); err != nil {
		t.Fatal(err)
	}
	if current, err := m.supervisedUnitCurrent(vector, config); err != nil || !current {
		t.Fatalf("recorded unit not current: %t %v", current, err)
	}
	if current, _ := m.supervisedUnitCurrent(vector, config+".other"); current {
		t.Fatal("a different config path reads as current")
	}
	if err := m.loadSupervised(); err != nil {
		t.Fatalf("load: %v", err)
	}
	first, _ := m.readSupervised()
	if !first.running() {
		t.Fatalf("not running after load: %+v", first)
	}
	t.Cleanup(func() { _ = m.unloadSupervised() })
	// Load again restarts: the old process goes, a new one runs.
	if err := m.loadSupervised(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	second, _ := m.readSupervised()
	if !second.running() || second.PID == first.PID || first.running() {
		t.Fatalf("reload did not replace the process: first=%+v second=%+v", first, second)
	}
	// writeSupervisedUnit keeps track of the running process.
	if _, err := m.writeSupervisedUnit(vector, config); err != nil {
		t.Fatal(err)
	}
	if kept, _ := m.readSupervised(); kept.PID != second.PID {
		t.Fatalf("rewriting the record lost the running pid: %+v", kept)
	}
	if err := m.unloadSupervised(); err != nil {
		t.Fatal(err)
	}
	stopped, err := m.readSupervised()
	if err != nil || stopped.PID != 0 || stopped.Program != vector || second.running() {
		t.Fatalf("after unload: %+v %v", stopped, err)
	}
	m.removeSupervised()
	if _, err := m.readSupervised(); !os.IsNotExist(err) {
		t.Fatalf("record not removed: %v", err)
	}
}

// A Vector that exits right away (a config it rejects, a locked data directory) is an error that
// names its output, not a started forwarder.
func TestSupervisedForwarderReportsAnImmediateExit(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	testenv.SetHome(t, t.TempDir())
	vector := filepath.Join(t.TempDir(), "vector")
	if err := os.WriteFile(vector, []byte("#!/bin/sh\necho 'config rejected' >&2\nexit 78\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Long, so a loaded machine that is slow to run the script still sees it exit inside the
	// check; Load returns as soon as it does.
	old := supervisedForwarderStartCheck
	supervisedForwarderStartCheck = 30 * time.Second
	t.Cleanup(func() { supervisedForwarderStartCheck = old })
	m := ForwarderManager{Kind: KindWindowsService, UserMode: true}
	if _, err := m.writeSupervisedUnit(vector, "vector.toml"); err != nil {
		t.Fatal(err)
	}
	err := m.loadSupervised()
	if err == nil || !strings.Contains(err.Error(), "exited right after it started") || !strings.Contains(err.Error(), m.supervisedLogPath()) {
		t.Fatalf("load = %v", err)
	}
	if st, _ := m.readSupervised(); st.PID != 0 {
		t.Fatalf("a dead pid stayed recorded: %+v", st)
	}
	if out, _ := os.ReadFile(m.supervisedLogPath()); !strings.Contains(string(out), "config rejected") {
		t.Fatalf("Vector's output was not kept: %q", out)
	}
}
