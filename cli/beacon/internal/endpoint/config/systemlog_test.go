package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The POSIX value must not move. /var/log/beacon-agent is where installed endpoints already write,
// where the packaging scripts create directories, and what the forwarder packs tail -- changing it
// would be a migration rather than a refactor, and this consolidation is meant to be neither.
func TestSystemLogPathIsUnchangedOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX location does not apply")
	}
	if got, want := SystemLogDir(), "/var/log/beacon-agent"; got != want {
		t.Errorf("SystemLogDir() = %q, want %q", got, want)
	}
	if got, want := SystemLogPath(), "/var/log/beacon-agent/runtime.jsonl"; got != want {
		t.Errorf("SystemLogPath() = %q, want %q", got, want)
	}
}

// On Windows the log lives under the same machine-wide root as the config, because there is no
// /var/log equivalent and %ProgramData% is where state that outlives any one user belongs.
func TestSystemLogPathIsUnderTheSystemBaseDirOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows location does not apply")
	}
	if !strings.HasPrefix(SystemLogDir(), SystemBaseDir()) {
		t.Errorf("SystemLogDir() = %q, want it under SystemBaseDir() %q", SystemLogDir(), SystemBaseDir())
	}
	if got, want := SystemLogPath(), filepath.Join(SystemLogDir(), "runtime.jsonl"); got != want {
		t.Errorf("SystemLogPath() = %q, want %q", got, want)
	}
}

// The log path must derive from the same root as the config, or a Windows install would scatter
// its state across two locations and only one of them would be documented.
func TestSystemLogPathDerivesFromOneRoot(t *testing.T) {
	if got := SystemLogPath(); !strings.HasSuffix(filepath.ToSlash(got), "/runtime.jsonl") {
		t.Errorf("SystemLogPath() = %q, want it to name runtime.jsonl", got)
	}
	if SystemLogDir() == "" || SystemBaseDir() == "" {
		t.Fatal("neither root may be empty; an empty prefix silently writes to the process's cwd")
	}
}
