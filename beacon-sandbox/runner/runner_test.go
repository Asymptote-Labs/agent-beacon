package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/image"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// The agent session runs in the scenario's working directory, so anything it writes must be read
// back from there. Reading relatively from the default directory silently found nothing whenever
// a scenario set `cwd:` -- s05-repo-task, the only such scenario, reported session_ok=false on an
// otherwise perfect run. That disabled precisely the signal that distinguishes "the agent never
// ran" from "Beacon missed the event", which is the distinction the whole verdict model rests on.
func TestSessionFilesResolveInsideTheScenarioWorkingDirectory(t *testing.T) {
	lay, sh := image.LinuxLayout(), posixShell{}
	withCwd := scenario.Scenario{ID: "s05-repo-task", Cwd: "repo"}
	got := sessionFile(withCwd, "claude-out.json", lay, sh)

	if !strings.HasSuffix(got, "/repo/claude-out.json") {
		t.Errorf("a scenario with cwd:repo must read from that directory, got %q", got)
	}
	if got == "claude-out.json" {
		t.Error("a bare relative name is what caused the original silent miss")
	}
	// It must be the same directory the session was launched in, or the two can drift apart.
	if !strings.HasPrefix(got, workDirFor(withCwd, lay, sh)) {
		t.Errorf("readback path %q is not under the session working dir %q",
			got, workDirFor(withCwd, lay, sh))
	}
}

// The common case must keep working: no cwd means the default working directory.
func TestSessionFilesDefaultToTheWorkDir(t *testing.T) {
	got := sessionFile(scenario.Scenario{ID: "s01-hello"}, "claude-out.json",
		image.LinuxLayout(), posixShell{})

	want := image.WorkDir + "/claude-out.json"
	if got != want {
		t.Errorf("sessionFile = %q, want %q", got, want)
	}
}

// Paths are consumed by a POSIX shell inside the guest, so they must use forward slashes
// regardless of the host that built them.
func TestSessionFilePathsAreAlwaysSlashed(t *testing.T) {
	got := sessionFile(scenario.Scenario{ID: "x", Cwd: "nested/dir"}, "claude-err.txt",
		image.LinuxLayout(), posixShell{})

	if strings.Contains(got, `\`) {
		t.Errorf("guest paths must be slash-separated, got %q", got)
	}
	if !strings.HasSuffix(got, "/nested/dir/claude-err.txt") {
		t.Errorf("nested cwd not resolved, got %q", got)
	}
}

// The mirror of the previous test for the other dialect. A Windows guest consumes these paths
// through Windows APIs, so a forward slash that "mostly works" would still be wrong in every
// verdict a human reads -- and a bare drive letter is a *relative* path, which is wrong in a way
// that silently writes to the wrong directory.
func TestWindowsSessionFilePathsUseBackslashesAndKeepTheDriveRoot(t *testing.T) {
	lay, sh := image.WindowsLayout(), powerShell{}
	got := sessionFile(scenario.Scenario{ID: "x", Cwd: "nested/dir"}, "claude-err.txt", lay, sh)

	if strings.Contains(got, "/") {
		t.Errorf("windows guest paths must be backslash-separated, got %q", got)
	}
	if !strings.HasSuffix(got, `\nested\dir\claude-err.txt`) {
		t.Errorf("nested cwd not resolved, got %q", got)
	}
	// "C:" without a separator means the current directory on that drive, not its root.
	if strings.HasPrefix(got, `C:`) && !strings.HasPrefix(got, `C:\`) {
		t.Errorf("a drive letter must keep its separator, got %q", got)
	}
}

// The argv scan is a disclosure claim, so it must rest on an observation actually made. An
// earlier version scanned only after `beacon ci exec` returned, when every process that might
// have held the key was already gone -- ARGV_CLEAN was close to vacuous. Cursor Bugbot flagged
// it on the first commit. These pin the properties that make the sampler meaningful.
func TestArgvSamplerRunsInBackgroundAndSamplesRepeatedly(t *testing.T) {
	got := posixShell{}.ArgvSampler()

	if !strings.Contains(got, "nohup") || !strings.HasSuffix(strings.TrimSpace(got), "ARGV_SAMPLER_STARTED") {
		t.Error("the sampler must detach so the session can run alongside it")
	}
	if !strings.Contains(got, "while [") || !strings.Contains(got, "sleep 1") {
		t.Error("a single sample cannot observe a live session; it must loop")
	}
	// Both sources, because a shell can hide argv from one but not the other.
	for _, want := range []string{"ps -eo args=", "/proc/[0-9]*/cmdline"} {
		if !strings.Contains(got, want) {
			t.Errorf("sampler should check %s", want)
		}
	}
}

// The matcher must never carry the key in its own argv, or the scan reports a leak it created.
func TestArgvSamplerNeverPutsTheKeyInAnArgv(t *testing.T) {
	got := posixShell{}.ArgvSampler()

	if strings.Contains(got, `grep -qF "$ANTHROPIC_API_KEY"`) || strings.Contains(got, "grep -F $ANTHROPIC_API_KEY") {
		t.Error("expanding the key into a matcher's argv is the self-poisoning bug")
	}
	if !strings.Contains(got, `ENVIRON["ANTHROPIC_API_KEY"]`) {
		t.Error("the key must be read from the environment inside awk, never passed as an argument")
	}
}

// With no key there is nothing to search for, so the sampler must say so rather than write a
// clean verdict. A check that cannot run must never read as one that passed.
func TestArgvSamplerReportsInvalidRatherThanCleanWithoutAKey(t *testing.T) {
	got := posixShell{}.ArgvSampler()

	if !strings.Contains(got, "ARGV_CHECK_INVALID_NO_KEY") {
		t.Error("a missing key must produce an explicit invalid marker")
	}
	guard := strings.Index(got, `if [ -z "${ANTHROPIC_API_KEY:-}" ]`)
	clean := strings.Index(got, "ARGV_CLEAN")
	if guard < 0 || clean < 0 || guard > clean {
		t.Error("the no-key guard must precede any path that can emit ARGV_CLEAN")
	}
}

// The sample count is evidence strength: one sample is far weaker than hundreds, so it is
// surfaced rather than collapsed into a boolean.
func TestSampleCountIsExtractedForTheRecord(t *testing.T) {
	cases := map[string]string{
		"ARGV_CLEAN samples=137":              "137",
		"ARGV_LEAK samples=4 via=proc":        "4",
		"ARGV_CHECK_INVALID_NO_KEY samples=0": "0",
		"":                                    "0",
		"garbage with no count":               "0",
	}
	for in, want := range cases {
		if got := sampleCountOf(in); got != want {
			t.Errorf("sampleCountOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The session script must exit with `beacon ci exec`'s status. Ending on `tail` made every session
// look successful to the exec layer no matter how ci exec fared, and nothing read CI_EXEC_RC
// either, so a collector that failed outright was indistinguishable from one that worked.
// Reported by the Copilot reviewer.
func TestSessionScriptPropagatesTheCollectorExitStatus(t *testing.T) {
	sh := posixShell{}
	sc := scenario.Scenario{ID: "s01", Prompt: "hi"}
	got := sh.CIExecSession("/work/runtime.jsonl", image.WorkDir, claudeFlags(sc, "hi", sh))

	if !strings.Contains(got, "rc=$?") || !strings.Contains(got, "exit $rc") {
		t.Errorf("the script must capture and re-raise ci exec's status:\n%s", got)
	}
	// The tail is for diagnostics and must not be the last thing determining the status.
	if strings.HasSuffix(strings.TrimSpace(got), "tail -c 400 ci-out.txt") {
		t.Error("ending on tail makes the exit status tail's, masking a failed ci exec")
	}
	if idx, tailIdx := strings.Index(got, "exit $rc"), strings.Index(got, "tail -c"); idx < tailIdx {
		t.Error("the tail should run before the exit so its output is still captured")
	}
}

// The sentinel probe's output must be unambiguous. `cat f || echo __MISSING__` produced empty
// stdout for both a failed guest exec and a legitimately empty sentinel file, so the two could not
// be told apart -- and the first is an infrastructure problem while the second is a real absence of
// agent work. An explicit marker on every branch removes the ambiguity.
func TestSentinelProbeAlwaysEmitsAMarker(t *testing.T) {
	probe := posixShell{}.ProbeFile("/home/agent/work/out.txt")

	for _, want := range []string{"__FOUND__", "__MISSING__"} {
		if !strings.Contains(probe, want) {
			t.Errorf("the probe must be able to emit %s", want)
		}
	}
	// Both branches are present, so no input leaves stdout empty.
	if !strings.Contains(probe, "if [ -f") || !strings.Contains(probe, "else") {
		t.Errorf("the probe must branch explicitly rather than relying on cat's exit status:\n%s", probe)
	}
	if strings.Contains(probe, "cat /home/agent/work/out.txt ||") {
		t.Error("the old `cat f || echo` form cannot distinguish an empty file from a failed exec")
	}
}

// Only a present boolean is evidence about the session. result["is_error"] is nil when the key is
// absent, and `nil == false` evaluates to false in Go, so the obvious form recorded a *known
// failure* for any result JSON lacking the field -- which then short-circuits sentinel-less
// scenarios to INCONCLUSIVE without evaluating a single expectation. The same invented-failure
// shape decodeSession avoids offline, reproduced in the capture path. Cursor Bugbot reported it.
func TestSessionOutcomeNeverInventsAFailure(t *testing.T) {
	cases := []struct {
		name              string
		result            map[string]any
		wantOK, wantKnown bool
	}{
		{"absent is_error", map[string]any{"subtype": "success"}, false, false},
		{"empty result", map[string]any{}, false, false},
		{"null is_error", map[string]any{"is_error": nil}, false, false},
		{"wrong type", map[string]any{"is_error": "false"}, false, false},
		{"real success", map[string]any{"is_error": false}, true, true},
		{"real failure", map[string]any{"is_error": true}, false, true},
	}
	for _, c := range cases {
		ok, known := sessionOutcome(c.result)
		if ok != c.wantOK || known != c.wantKnown {
			t.Errorf("%s: got ok=%v known=%v, want ok=%v known=%v",
				c.name, ok, known, c.wantOK, c.wantKnown)
		}
		// The dangerous combination: a confident failure conjured from a missing field.
		if c.name != "real failure" && known && !ok {
			t.Errorf("%s: must not read as a known failure", c.name)
		}
	}
}

// The sampler is a shell program assembled in Go, so a syntax error in it is invisible to the
// compiler and would surface as a silent "the check did not run" -- which the verdict reports as
// unverified rather than failing loudly. Checked here with the host's own shell.
func TestArgvSamplerScriptIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	f := filepath.Join(t.TempDir(), "sampler.sh")
	if err := os.WriteFile(f, []byte(posixShell{}.ArgvSampler()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-n", f).CombinedOutput()
	if err != nil {
		t.Fatalf("generated sampler is not valid shell: %v\n%s", err, out)
	}
	// The inner sampler body is a heredoc, which `sh -n` does not parse. Extract and check it too,
	// since that is where the polling loop lives.
	body := posixShell{}.ArgvSampler()
	start := strings.Index(body, "#!/bin/sh")
	end := strings.Index(body, "\nSAMPLER\n")
	if start < 0 || end < 0 || end < start {
		t.Fatal("could not locate the inner sampler body; this test is no longer checking it")
	}
	inner := filepath.Join(t.TempDir(), "inner.sh")
	if err := os.WriteFile(inner, []byte(body[start:end]), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", inner).CombinedOutput(); err != nil {
		t.Fatalf("inner sampler body is not valid shell: %v\n%s", err, out)
	}
}

// A clean verdict from a sampler that never saw an agent process must read as unverified. This is
// the invariant the whole tool rests on: silence means verified-clean, never "we did not look."
func TestArgvSamplerEmitsWhetherItSawTheAgent(t *testing.T) {
	script := posixShell{}.ArgvSampler()
	for _, want := range []string{"saw_agent=0", "saw_agent=$saw_agent", "*claude*"} {
		if !strings.Contains(script, want) {
			t.Errorf("sampler no longer records whether the agent was in view (missing %q)", want)
		}
	}
}
