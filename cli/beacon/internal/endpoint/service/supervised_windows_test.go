//go:build windows

package service

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A pid is the program it was started as only while that program is what runs under it: a pid
// Windows handed to another process is never reported running, so it is never stopped.
func TestPidRunsProgramChecksTheImage(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	if !pidRunsProgram(pid, self) {
		t.Fatalf("this process is not %s", self)
	}
	if !pidRunsProgram(pid, strings.ToUpper(self)) {
		t.Fatal("the image comparison is case-sensitive")
	}
	if pidRunsProgram(pid, self+".other.exe") {
		t.Fatal("another program's path matched this process")
	}

	cmd := exec.Command(stubCollectorPath(t))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if pidRunsProgram(cmd.Process.Pid, `C:\Program Files\Beacon\bin\vector.exe`) {
		t.Fatal("a recycled pid read as the forwarder")
	}
}
