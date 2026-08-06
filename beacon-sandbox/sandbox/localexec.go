package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LocalExec is the Provider for "the machine this process is already on".
//
// It exists because Modal has no Windows, and the alternatives all cost something worse than
// this does. A GitHub Actions Windows runner is already a disposable machine with a real
// Service Control Manager, administrator rights and no UAC -- everything an endpoint install
// needs. So rather than rent an instance from inside it, the runner *is* the instance, and
// this backend just runs commands where it stands.
//
// The consequence is the important part. Every other Provider hands back a separate machine,
// which is what lets hostguard prove Beacon was never installed outside the sandbox. Here
// guest and host are the same machine, so that proof has to come from somewhere else -- see
// MutatesHost and requireDisposable.
type LocalExec struct {
	// shell is the interpreter Exec hands scripts to.
	shell     string
	shellArgs []string
	platform  Platform
	// workdir is the base directory instances report; scripts may cd elsewhere.
	workdir string
}

// LocalExecOptions configures the backend.
type LocalExecOptions struct {
	// Workdir is the base directory for launched instances. Empty uses the process's own.
	Workdir string
	// AllowHostMutation runs scenarios on a machine this tool cannot prove is disposable.
	//
	// Off by default, and the default is the safe one: a scenario installs Beacon for real,
	// registers services and rewrites agent-runtime settings files. On a workstation that is
	// not a test, it is damage. Contributors who genuinely want this -- a throwaway VM they
	// own -- have to say so.
	AllowHostMutation bool
}

type localImage struct{ ref string }

func (l localImage) Ref() string { return l.ref }

type localInstance struct{ id string }

func (l localInstance) ID() string { return l.id }

// NewLocalExec returns a backend for the current machine, refusing unless the machine is
// provably disposable or the caller has explicitly accepted the risk.
func NewLocalExec(opts LocalExecOptions) (*LocalExec, error) {
	if err := requireDisposable(opts.AllowHostMutation); err != nil {
		return nil, err
	}
	l := &LocalExec{workdir: opts.Workdir}
	if runtime.GOOS == "windows" {
		l.platform = PlatformWindows
		// PowerShell 7, and not Windows PowerShell 5.1 as a fallback.
		//
		// 5.1's `>` writes UTF-16LE, which would silently corrupt every JSON artifact this tool
		// parses -- claude-out.json above all, the file that says whether the session succeeded.
		// A quiet encoding difference producing "the agent failed" is worse than refusing here,
		// and pwsh is what the runner images ship and what Claude Code itself uses on Windows.
		if _, err := exec.LookPath("pwsh"); err != nil {
			return nil, fmt.Errorf("pwsh (PowerShell 7+) is required on Windows and is not on " +
				"PATH; Windows PowerShell 5.1 redirects output as UTF-16, which corrupts the " +
				"JSON artifacts this tool reads")
		}
		l.shell = "pwsh"
		// -NoProfile matters for reproducibility: a profile can alter PATH, aliases and the
		// error-action preference, so a run would otherwise depend on the host's dotfiles.
		l.shellArgs = []string{"-NoProfile", "-NonInteractive", "-Command"}
		return l, nil
	}
	l.platform = PlatformPosix
	l.shell = "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		l.shell = "sh"
	}
	l.shellArgs = []string{"-c"}
	return l, nil
}

// requireDisposable refuses to run on a machine whose destruction is not somebody else's job.
//
// GITHUB_ACTIONS alone is not enough. A self-hosted runner is a real machine that persists
// between jobs, so an install performed there survives the run and the next job inherits it --
// which is exactly the escape hostguard exists to catch, just relocated. RUNNER_ENVIRONMENT is
// GitHub's own answer to the question, so it is what gets asked.
func requireDisposable(allow bool) error {
	if allow {
		return nil
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return fmt.Errorf("the local provider runs scenarios on this machine, and a scenario " +
			"installs Beacon, registers services and rewrites agent-runtime settings for real; " +
			"it is meant for a disposable CI runner. Use `--provider github` to dispatch a run to " +
			"one, or pass --allow-host-mutation if this machine is genuinely throwaway")
	}
	if env := os.Getenv("RUNNER_ENVIRONMENT"); env != "" && env != "github-hosted" {
		return fmt.Errorf("this is a %s Actions runner, which persists between jobs, so an "+
			"install performed here would outlive the run; pass --allow-host-mutation only if "+
			"this runner is genuinely disposable", env)
	}
	return nil
}

// DisposabilityEvidence describes why this machine was accepted, for run metadata.
//
// Recorded rather than implied. hostguard's comparison cannot mean anything here, so the
// verdict has to say what it relied on instead -- and "the operator asserted it" is a
// materially weaker claim than "GitHub hands out a fresh VM per job", which a reader of the
// verdict is entitled to tell apart.
func DisposabilityEvidence() string {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		env := os.Getenv("RUNNER_ENVIRONMENT")
		if env == "" {
			env = "unreported"
		}
		if env == "github-hosted" {
			return "github-hosted ephemeral runner (fresh VM per job)"
		}
		return "GitHub Actions runner, environment " + env + ", accepted by explicit operator opt-in"
	}
	return "operator asserted this machine is disposable (--allow-host-mutation)"
}

func (l *LocalExec) Name() string { return "local" }

func (l *LocalExec) Platform() Platform { return l.platform }

// MutatesHost is true, and saying so is the whole point of the flag: the runner reads it and
// swaps hostguard's comparison for a disposability assertion instead of reporting a false
// escape.
func (l *LocalExec) MutatesHost() bool { return true }

// EnsureImage is a no-op. Provisioning happened before this process started -- the CI job
// installed the agent runtime and built the binaries under test -- so there is no image to
// build and nothing to cache. The spec's layers are reported in the ref so a verdict still
// records what environment was expected.
func (l *LocalExec) EnsureImage(_ context.Context, spec ImageSpec) (Image, error) {
	base := spec.Base
	if base == "" {
		base = runtime.GOOS + "/" + runtime.GOARCH
	}
	return localImage{ref: "local:" + base}, nil
}

// Snapshot is not available: there is no instance boundary to snapshot across. Returning an
// error rather than a silent no-op keeps a caller that depends on snapshotting from believing
// it got one.
func (l *LocalExec) Snapshot(_ context.Context, _ Instance) (Image, error) {
	return nil, fmt.Errorf("the local provider cannot snapshot: the guest is this machine")
}

// Launch records the workdir and returns a handle. Nothing starts, because the machine is
// already running.
func (l *LocalExec) Launch(_ context.Context, _ Image, spec LaunchSpec) (Instance, error) {
	if spec.Workdir != "" {
		l.workdir = spec.Workdir
	}
	// Secrets are deliberately not applied to this process's environment. They are already
	// present here -- the CI job injected them -- and mutating the parent's environment would
	// leak them into every later command, including ones a scenario never meant to see them.
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	return localInstance{id: fmt.Sprintf("local:%s:%d", host, os.Getpid())}, nil
}

// Exec runs a script through the platform's shell.
func (l *LocalExec) Exec(ctx context.Context, _ Instance, script string, opts ExecOpts) (Result, error) {
	// User switching is not supported: this process cannot become another user without
	// privileges it has no business holding, and su/runas would need a password. A scenario
	// that needs an unprivileged account must have the CI job create one and run the whole
	// tool as them. Reported rather than ignored, because silently running as the wrong user
	// is how a scenario ends up verifying root's unused settings file.
	if opts.User != "" && !l.currentUserIs(opts.User) {
		return Result{}, fmt.Errorf("the local provider cannot switch to user %q; run the "+
			"harness as that user instead", opts.User)
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, l.shell, append(l.shellArgs, script)...)
	cmd.Dir = l.resolveDir(opts.Dir)
	cmd.Env = l.execEnv(opts)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	// The deadline is checked before the error is classified, and the order matters. When the
	// context expires, CommandContext kills the child and Run returns an *ExitError like any other
	// non-zero exit -- so classifying first would record a killed session as an exit status, and
	// the verdict would read it as Beacon failing to capture rather than as the harness never
	// having finished the run. "The command failed" and "we never ran it to completion" must not
	// collapse into the same result. Reported by Cursor Bugbot.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("exec %s did not complete (%w): the command was killed after %s",
			l.shell, ctxErr, opts.Timeout)
	}
	// An ExitError carries the status and is not a failure of Exec itself; every caller reads
	// ExitCode and decides what it means. Anything else -- the shell missing, for instance -- is a
	// harness failure and must surface as one.
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("exec %s: %w", l.shell, err)
	}
	return res, nil
}

// execEnv builds the child environment, honoring the same ExecOpts fields the remote backends do.
func (l *LocalExec) execEnv(opts ExecOpts) []string {
	env := os.Environ()
	if opts.HomeDir != "" {
		env = append(env, "HOME="+opts.HomeDir)
		if l.platform.IsWindows() {
			// Windows programs read USERPROFILE, not HOME. Setting only HOME would leave a
			// scenario's home-relative paths pointing at the real profile.
			env = append(env, "USERPROFILE="+opts.HomeDir)
		}
	}
	if len(opts.PathPrepend) > 0 {
		sep := ":"
		if l.platform.IsWindows() {
			sep = ";"
		}
		env = append(env, "PATH="+strings.Join(opts.PathPrepend, sep)+sep+os.Getenv("PATH"))
	}
	return env
}

func (l *LocalExec) resolveDir(dir string) string {
	switch {
	case dir != "":
		return dir
	case l.workdir != "":
		return l.workdir
	default:
		return ""
	}
}

// currentUserIs reports whether the requested user is the one already running.
func (l *LocalExec) currentUserIs(want string) bool {
	for _, key := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(key); v != "" && strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// Put and Get are local copies. They keep the Provider shape so the runner does not care
// which backend it has, and they still cross a boundary worth crossing: run artifacts land in
// the run directory rather than being read from wherever the scenario left them.
func (l *LocalExec) Put(_ context.Context, _ Instance, localPath, remotePath string) error {
	return copyFile(localPath, remotePath)
}

func (l *LocalExec) Get(_ context.Context, _ Instance, remotePath, localPath string) error {
	return copyFile(remotePath, localPath)
}

// Terminate is a no-op: destroying this machine is the CI job's business, not ours.
func (l *LocalExec) Terminate(_ context.Context, _ Instance) error { return nil }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	// 0600, matching the run directory: collected artifacts retain prompt text and command
	// output, and on a shared machine a wider mode hands that to every local user.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
