//go:build windows

package service

import (
	"strings"
	"testing"
)

// TestServiceBinaryPathIsEscapedTheWayTheSCMReadsIt guards the upgrade path.
//
// A fresh install goes through CreateService, which escapes the ImagePath itself. An upgrade goes
// through UpdateConfig, which takes one already-rendered string -- so this is the only place the
// two can disagree, and a disagreement here does not show up until the service is next started,
// long after the upgrade reported success.
//
// The specific mistake this replaced was Go's %q, which escapes for a Go string literal rather than
// for the SCM: every backslash doubled, and the default install path lives under %ProgramFiles%.
func TestServiceBinaryPathIsEscapedTheWayTheSCMReadsIt(t *testing.T) {
	const (
		program = `C:\Program Files\Beacon\bin\beacon-otelcol.exe`
		config  = `C:\ProgramData\Beacon\Endpoint\collector.yaml`
	)
	got := serviceBinaryPath(program, config)

	// Quoted because of the space, and left alone because there is not one.
	want := `"` + program + `" --config ` + config
	if got != want {
		t.Fatalf("serviceBinaryPath = %s, want %s", got, want)
	}
	if strings.Contains(got, `\\`) {
		t.Fatalf("serviceBinaryPath doubled a backslash: %s; "+
			"the SCM does not unescape those, so the service would fail to start after an upgrade", got)
	}
	// The path has to survive as one argument. Anything that splits it at the space leaves the SCM
	// looking for C:\Program.
	if !strings.HasPrefix(got, `"`+program+`"`) {
		t.Fatalf("serviceBinaryPath left a path containing a space unquoted: %s", got)
	}
}

func TestServiceBinaryPathQuotesOnlyWhenItMustAndAlwaysCarriesTheConfig(t *testing.T) {
	got := serviceBinaryPath(`C:\Beacon\beacon-otelcol.exe`, `C:\Beacon\collector.yaml`)
	if got != `C:\Beacon\beacon-otelcol.exe --config C:\Beacon\collector.yaml` {
		t.Fatalf("serviceBinaryPath = %s", got)
	}
	if !strings.Contains(got, "--config") {
		t.Fatalf("serviceBinaryPath dropped the config flag: %s; "+
			"the collector would start with its default configuration and write nowhere Beacon reads", got)
	}
}
