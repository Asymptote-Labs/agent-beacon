package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestLocalExec builds a provider without the disposability gate, which is the point of a
// separate constructor path in tests: the gate exists to stop a scenario installing Beacon on a
// workstation, and these tests run trivial shell commands rather than scenarios.
func newTestLocalExec(t *testing.T) *LocalExec {
	t.Helper()
	l, err := NewLocalExec(LocalExecOptions{AllowHostMutation: true})
	if err != nil {
		t.Skipf("no usable shell on this host: %v", err)
	}
	return l
}

// The gate is the only thing standing between `--provider local` and a scenario installing Beacon
// and registering services on somebody's workstation, so its outcomes are pinned.
func TestRequireDisposableRefusesAnUnprovableHost(t *testing.T) {
	cases := []struct {
		name      string
		actions   string
		runnerEnv string
		allow     bool
		wantErr   bool
	}{
		{"workstation", "", "", false, true},
		{"workstation with opt-in", "", "", true, false},
		// Self-hosted runners persist between jobs, so an install there outlives the run -- the same
		// escape the host guard exists to catch, relocated.
		{"self-hosted runner", "true", "self-hosted", false, true},
		{"self-hosted with opt-in", "true", "self-hosted", true, false},
		{"github-hosted runner", "true", "github-hosted", false, false},
		// Older Actions runners did not report the environment; GITHUB_ACTIONS is already the
		// stronger signal, so an unreported value is accepted.
		{"unreported environment", "true", "", false, false},
	}
	for _, c := range cases {
		t.Setenv("GITHUB_ACTIONS", c.actions)
		t.Setenv("RUNNER_ENVIRONMENT", c.runnerEnv)

		err := requireDisposable(c.allow)
		if c.wantErr && err == nil {
			t.Errorf("%s: expected a refusal", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: expected acceptance, got %v", c.name, err)
		}
	}
}

// The evidence a verdict reports must distinguish "GitHub hands out a fresh VM per job" from "the
// operator asserted it". They are materially different claims, and a reader of a verdict that
// cannot compare host state is entitled to know which one it rested on.
func TestDisposabilityEvidenceNamesItsBasis(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	if got := DisposabilityEvidence(); !strings.Contains(got, "ephemeral") {
		t.Errorf("a github-hosted runner should say so, got %q", got)
	}

	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("RUNNER_ENVIRONMENT", "")
	got := DisposabilityEvidence()
	if !strings.Contains(got, "asserted") {
		t.Errorf("an operator opt-in must be named as such, got %q", got)
	}
	if strings.Contains(got, "ephemeral") {
		t.Error("an asserted host must not borrow the wording of a provably ephemeral one")
	}
}

// A non-zero exit is a finding about the thing under test; a timeout is the harness failing to
// finish. They must not collapse into one result -- CommandContext kills the child and Run returns
// an *ExitError either way, so classifying the error without consulting the deadline recorded a
// killed session as an exit status and the verdict read it as a capture failure.
func TestExecDistinguishesATimeoutFromANonZeroExit(t *testing.T) {
	l := newTestLocalExec(t)
	inst := localInstance{id: "test"}

	// A command that exits non-zero promptly: a result, not an error.
	script := "exit 3"
	if l.platform.IsWindows() {
		script = "exit 3"
	}
	res, err := l.Exec(context.Background(), inst, script, ExecOpts{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an Exec error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}

	// A command that outlives its deadline: an error, not a result.
	sleep := "sleep 10"
	if l.platform.IsWindows() {
		sleep = "Start-Sleep -Seconds 10"
	}
	res, err = l.Exec(context.Background(), inst, sleep, ExecOpts{Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatalf("a timeout must surface as an error, got a result with exit code %d", res.ExitCode)
	}
	if !strings.Contains(err.Error(), "did not complete") {
		t.Errorf("the error should say the command never finished, got %v", err)
	}
}

// Stdout and stderr are captured separately, because every verdict is built from them and the
// runner reads each one for different signals.
func TestExecCapturesBothStreams(t *testing.T) {
	l := newTestLocalExec(t)
	inst := localInstance{id: "test"}

	script := "echo out; echo err 1>&2"
	if l.platform.IsWindows() {
		script = "Write-Output 'out'; [Console]::Error.WriteLine('err')"
	}
	res, err := l.Exec(context.Background(), inst, script, ExecOpts{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "out") {
		t.Errorf("stdout = %q, want it to contain \"out\"", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "err") {
		t.Errorf("stderr = %q, want it to contain \"err\"", res.Stderr)
	}
}

// Snapshot must fail rather than quietly succeed. A caller that believes it snapshotted, and did
// not, would reuse a stale environment and attribute the result to the wrong build.
func TestSnapshotIsRefusedRatherThanFaked(t *testing.T) {
	l := newTestLocalExec(t)
	if _, err := l.Snapshot(context.Background(), localInstance{id: "test"}); err == nil {
		t.Error("Snapshot must report that it cannot snapshot this backend")
	}
}

// Put and Get carry collected artifacts into the run directory, and those retain prompt text and
// command output -- so the copies must not be readable by every local user.
func TestPutAndGetCopyWithOwnerOnlyMode(t *testing.T) {
	l := newTestLocalExec(t)
	inst := localInstance{id: "test"}
	dir := t.TempDir()

	src := filepath.Join(dir, "src.jsonl")
	if err := os.WriteFile(src, []byte(`{"event":"one"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "dst.jsonl")
	if err := l.Put(context.Background(), inst, src, dst); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || !strings.Contains(string(b), "one") {
		t.Fatalf("Put did not copy the content: %v %q", err, b)
	}

	back := filepath.Join(dir, "back.jsonl")
	if err := l.Get(context.Background(), inst, dst, back); err != nil {
		t.Fatalf("Get: %v", err)
	}
	info, err := os.Stat(back)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not implement Unix permission bits, so the mode assertion is meaningful only
	// where it is enforced. Asserting it there anyway would fail for a reason unrelated to the
	// property being protected.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("collected artifacts must be owner-only, got %v", info.Mode().Perm())
	}
}
