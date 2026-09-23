//go:build !windows

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestPidRunsProgramReadsProcCmdline(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "4242"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A shebang script: the interpreter is argv[0] and the program is argv[1].
	cmdline := "/bin/sh\x00/tmp/beacon-fake-collector\x00--config\x00/etc/beacon/endpoint/otelcol.yaml\x00"
	if err := os.WriteFile(filepath.Join(root, "4242", "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })

	if !pidRunsProgram(4242, "/tmp/beacon-fake-collector") {
		t.Error("the recorded program is argv[1]; want a match")
	}
	if pidRunsProgram(4242, "/opt/beacon/bin/beacon-otelcol") {
		t.Error("a pid running something else must not match")
	}
	if pidRunsProgram(4242, "/tmp/beacon-fake") {
		t.Error("a prefix of an argument is not the program")
	}
	if pidRunsProgram(999, "/tmp/beacon-fake-collector") {
		t.Error("procfs has no such pid; want no match")
	}

	procRoot = filepath.Join(root, "missing")
	if !pidRunsProgram(4242, "/opt/beacon/bin/beacon-otelcol") {
		t.Error("without procfs there is nothing to ask; want the signal-0 answer to stand")
	}
}

// A pidfile that outlived its collector names whatever the kernel gave that pid to next. Status
// must not call that process the collector, and unload must not signal it.
func TestSupervisedIgnoresARecycledPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the identity check reads procfs")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	m := Manager{UserMode: true, Kind: KindSupervised}

	stranger := exec.Command(stubCollectorPath(t), "--config", "unrelated")
	if err := stranger.Start(); err != nil {
		t.Fatalf("start the unrelated process: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = stranger.Wait(); close(exited) }()
	t.Cleanup(func() { _ = stranger.Process.Kill(); <-exited })

	b := supervisedBackend{}
	if _, err := m.WriteUnit("/opt/beacon/bin/beacon-otelcol", "/tmp/otelcol.yaml"); err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}
	if err := b.write(true, supervisedState{
		PID:            stranger.Process.Pid,
		Program:        "/opt/beacon/bin/beacon-otelcol",
		ConfigPath:     "/tmp/otelcol.yaml",
		StartedProgram: "/opt/beacon/bin/beacon-otelcol",
	}); err != nil {
		t.Fatal(err)
	}

	if st := m.Status(); st.Running {
		t.Fatalf("pid %d is not the recorded collector, but status reports it running: %#v",
			stranger.Process.Pid, st)
	}
	if err := m.Unload(); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	select {
	case <-exited:
		t.Fatal("unload signalled a process that is not the collector")
	case <-time.After(200 * time.Millisecond):
	}
	st, err := b.read(true)
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != 0 {
		t.Fatalf("unload should forget a pid that is not the collector, pidfile still holds %s",
			strconv.Itoa(st.PID))
	}
}

// A reinstall records the new program while the old collector is still running, then restarts.
// Unload has to recognise the old collector by what it was started as, not by the program just
// recorded, or it leaves the old collector running beside the new one.
func TestSupervisedUnloadStopsACollectorStartedAsAnotherProgram(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the identity check reads procfs")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	m := Manager{UserMode: true, Kind: KindSupervised}

	if _, err := m.WriteUnit(stubCollectorPath(t), "300"); err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}
	if err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	old, err := supervisedBackend{}.read(true)
	if err != nil || old.PID <= 0 {
		t.Fatalf("no collector recorded after Load: %#v %v", old, err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(old.PID); err == nil {
			_ = p.Kill()
		}
	})

	if _, err := m.WriteUnit("/tmp/beacon-next-collector", "/tmp/otelcol.yaml"); err != nil {
		t.Fatalf("second WriteUnit: %v", err)
	}
	if st := m.Status(); !st.Running {
		t.Fatalf("the old collector is still running, but status lost it: %#v", st)
	}
	if err := m.Unload(); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	// Polled: the stopped collector is reaped by Load's goroutine, and until then it is a zombie
	// that signal 0 still finds.
	deadline := time.Now().Add(2 * time.Second)
	for pidAlive(old.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("unload left the collector started before the reinstall (pid %d) running", old.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
