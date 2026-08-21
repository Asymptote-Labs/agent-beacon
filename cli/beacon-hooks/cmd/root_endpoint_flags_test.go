package cmd

import (
	"os"
	"testing"
)

// withFlags sets the package-level flag values for one test and restores them after.
//
// Cobra binds them to globals, so a test that forgot to restore would leak into the next one and the
// failure would land somewhere unrelated.
func withFlags(t *testing.T, logValue, configValue, cliValue string) {
	t.Helper()
	prevLog, prevConfig, prevCLI := logFlag, configFlag, cliFlag
	t.Cleanup(func() { logFlag, configFlag, cliFlag = prevLog, prevConfig, prevCLI })
	logFlag, configFlag, cliFlag = logValue, configValue, cliValue
}

func TestEndpointFlagsReachTheReadersThatOnlyLookAtTheEnvironment(t *testing.T) {
	t.Setenv("BEACON_ENDPOINT_LOG", "")
	t.Setenv("BEACON_ENDPOINT_CONFIG", "")
	t.Setenv("BEACON_ENDPOINT_CLI", "")
	t.Setenv("BEACON_ENDPOINT_MODE", "")
	withFlags(t, `C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl`, `C:\ProgramData\Beacon\Endpoint\config.json`, `C:\Program Files\Beacon\bin\beacon.exe`)

	applyEndpointFlagsToEnv()

	for key, want := range map[string]string{
		"BEACON_ENDPOINT_LOG":    `C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl`,
		"BEACON_ENDPOINT_CONFIG": `C:\ProgramData\Beacon\Endpoint\config.json`,
		"BEACON_ENDPOINT_CLI":    `C:\Program Files\Beacon\bin\beacon.exe`,
		// Inferred, not passed. Several readers gate on it before falling back to a default log
		// path, so without it a hook installed by flags alone would write to the wrong file.
		"BEACON_ENDPOINT_MODE": "1",
	} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestAFlagBeatsAnInheritedVariable covers the state a repair passes through.
//
// An already-installed POSIX hook carries these values as an inline env prefix. A repair rewrites the
// command to use flags, and until every runtime config has been rewritten a single invocation can
// carry both. The flag is the more specific statement of intent: something wrote it into this exact
// command, while the variable could have come from anywhere in the parent environment.
func TestAFlagBeatsAnInheritedVariable(t *testing.T) {
	t.Setenv("BEACON_ENDPOINT_LOG", "/var/log/beacon-agent/runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	withFlags(t, "/home/dev/.beacon/endpoint/logs/runtime.jsonl", "", "")

	applyEndpointFlagsToEnv()

	if got := os.Getenv("BEACON_ENDPOINT_LOG"); got != "/home/dev/.beacon/endpoint/logs/runtime.jsonl" {
		t.Fatalf("BEACON_ENDPOINT_LOG = %q, want the flag value to win", got)
	}
}

// TestNoFlagsChangesNothing is what keeps every already-installed hook working.
//
// Those hooks pass no flags at all. If this function invented an endpoint mode or a log path for
// them, it would redirect a working POSIX install to a different file on the next hook call.
func TestNoFlagsChangesNothing(t *testing.T) {
	t.Setenv("BEACON_ENDPOINT_LOG", "/var/log/beacon-agent/runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_CONFIG", "")
	t.Setenv("BEACON_ENDPOINT_CLI", "")
	t.Setenv("BEACON_ENDPOINT_MODE", "")
	withFlags(t, "", "", "")

	applyEndpointFlagsToEnv()

	if got := os.Getenv("BEACON_ENDPOINT_LOG"); got != "/var/log/beacon-agent/runtime.jsonl" {
		t.Fatalf("BEACON_ENDPOINT_LOG = %q, want it untouched", got)
	}
	if got := os.Getenv("BEACON_ENDPOINT_MODE"); got != "" {
		t.Fatalf("BEACON_ENDPOINT_MODE = %q; a hook that passed no flags is not necessarily an "+
			"endpoint hook, and claiming otherwise gives it a default log path it should not have", got)
	}
}

// TestWhitespaceOnlyFlagsAreNotValues guards the shape a shell leaves behind.
//
// A command string that lost its value to a quoting mistake produces an empty or blank flag rather
// than an absent one. Treating that as a path would point a hook at "" and lose its events silently.
func TestWhitespaceOnlyFlagsAreNotValues(t *testing.T) {
	t.Setenv("BEACON_ENDPOINT_LOG", "/var/log/beacon-agent/runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "")
	withFlags(t, "   ", "", "")

	applyEndpointFlagsToEnv()

	if got := os.Getenv("BEACON_ENDPOINT_LOG"); got != "/var/log/beacon-agent/runtime.jsonl" {
		t.Fatalf("BEACON_ENDPOINT_LOG = %q, want a blank flag ignored rather than applied", got)
	}
	if got := os.Getenv("BEACON_ENDPOINT_MODE"); got != "" {
		t.Fatalf("BEACON_ENDPOINT_MODE = %q, want a blank flag not to imply endpoint mode", got)
	}
}
