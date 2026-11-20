package diagnostics

import (
	"runtime"
	"testing"
)

// Windows has no sudo. An action there names the command and the elevated shell it needs; every
// other platform keeps the sudo prefix it always had.
func TestElevatedCommandNamesThePlatformsElevation(t *testing.T) {
	const command = "beacon endpoint repair --system"
	for goos, want := range map[string]string{
		"linux":   "sudo beacon endpoint repair --system",
		"darwin":  "sudo beacon endpoint repair --system",
		"windows": "beacon endpoint repair --system (from an elevated shell)",
	} {
		if got := elevatedCommandFor(goos, command); got != want {
			t.Errorf("elevatedCommandFor(%q) = %q, want %q", goos, got, want)
		}
	}
	if got := ElevatedCommand(command); got != elevatedCommandFor(runtime.GOOS, command) {
		t.Errorf("ElevatedCommand = %q, want this platform's form", got)
	}
}

// The repair actions this package already named keep their exact text on every platform.
func TestRepairSystemActionsKeepTheirText(t *testing.T) {
	want := "sudo beacon endpoint repair --system"
	wantShared := "sudo beacon endpoint repair --system --log-access shared"
	if runtime.GOOS == "windows" {
		want = "beacon endpoint repair --system (from an elevated shell)"
		wantShared = "beacon endpoint repair --system --log-access shared (from an elevated shell)"
	}
	if repairSystemAction != want {
		t.Errorf("repairSystemAction = %q, want %q", repairSystemAction, want)
	}
	if got := repairSystemWith("--log-access shared"); got != wantShared {
		t.Errorf("repairSystemWith = %q, want %q", got, wantShared)
	}
}
