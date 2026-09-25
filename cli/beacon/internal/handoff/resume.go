package handoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Resume modes.
const (
	// ModeNative reopens the session itself in its own runtime, with its full context.
	ModeNative = "native"
	// ModeNewSession starts a fresh session that is pointed at a brief of the old one.
	ModeNewSession = "new_session"
)

// Why a resume starts a new session instead of reopening the old one.
const (
	ReasonRequested      = "requested"
	ReasonOtherRuntime   = "other_runtime"
	ReasonNotResumable   = "not_resumable"
	ReasonSessionGone    = "session_file_missing"
	ReasonFromRuntimeLog = "runtime_log_only"
	ReasonOtherDirectory = "other_directory"
)

// Plan is a resolved resume: what will run, where, and why.
type Plan struct {
	Mode       string   `json:"mode"`
	Reason     string   `json:"reason,omitempty"`
	Source     Session  `json:"source"`
	Target     string   `json:"target"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	Dir        string   `json:"dir"`
	// BriefPath is where the brief for a new session is written. It is empty for a native resume.
	BriefPath string `json:"brief_path,omitempty"`
	// Env is what the runtime's environment differs by: a variable set to a value is set, one set
	// to "" is removed.
	Env map[string]string `json:"env,omitempty"`
}

// PlanOptions steer how a session is picked up.
type PlanOptions struct {
	// Target is the runtime to continue in. Empty means the session's own runtime.
	Target string
	// ForceNew starts a new session even when the session could be reopened.
	ForceNew bool
	// Dir overrides the directory the runtime starts in.
	Dir string
	// FromRuntimeLog marks a session known only from Beacon's runtime log, which no runtime can
	// reopen.
	FromRuntimeLog bool
	// BriefPath is where a new session's brief will be written.
	BriefPath string
	// LookPath resolves an executable on PATH; exec.LookPath outside tests.
	LookPath func(string) (string, error)
}

// ErrRuntimeNotInstalled reports that the runtime to continue in is not on PATH.
var ErrRuntimeNotInstalled = errors.New("runtime is not installed")

// PlanResume decides how to pick session up. It reopens the session natively when it can: same
// runtime, a runtime CLI that can load it, and its session file still present. Otherwise it starts
// a new session in the target runtime, pointed at a brief. The target runtime must be on PATH.
func PlanResume(session Session, opts PlanOptions) (Plan, error) {
	target := session.Harness
	if opts.Target != "" {
		target = opts.Target
	}
	targetCommand, ok := commandFor(target)
	if !ok {
		return Plan{}, fmt.Errorf("%s sessions cannot be started by beacon handoff (supported: %s)", target, strings.Join(StartableNames(), ", "))
	}
	dir, err := resumeDir(session, opts.Dir)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Source: session, Target: target, Dir: dir}

	executable, err := opts.LookPath(targetCommand.Executable)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %s (%s was not found on PATH)", ErrRuntimeNotInstalled, RuntimeLabel(target), targetCommand.Executable)
	}
	plan.Executable = executable
	if len(targetCommand.Env) > 0 {
		plan.Env = make(map[string]string, len(targetCommand.Env))
		for name, value := range targetCommand.Env {
			plan.Env[name] = value
		}
	}
	reason := nativeBlocker(session, target, opts)
	if reason == "" {
		plan.Mode, plan.Args = ModeNative, nativeArgs(targetCommand, session)
		return plan, nil
	}
	if opts.BriefPath == "" {
		return Plan{}, errors.New("a new session needs a brief path")
	}
	// The runtime reads the brief from its own directory, so a relative path would name another file.
	if !filepath.IsAbs(opts.BriefPath) {
		return Plan{}, fmt.Errorf("brief path %s is not absolute", opts.BriefPath)
	}
	plan.Mode, plan.Reason = ModeNewSession, reason
	plan.BriefPath = opts.BriefPath
	if targetCommand.NewSession == nil {
		return Plan{}, fmt.Errorf("%s cannot start a new session from a brief (%s)", RuntimeLabel(target), ReasonText(reason))
	}
	plan.Args = targetCommand.NewSession(NewSessionPrompt(session, opts.BriefPath))
	return plan, nil
}

func nativeArgs(command *runtimeCommand, session Session) []string {
	args, _ := command.Resume(session)
	return args
}

// nativeBlocker returns why session cannot be reopened in target, or "" when it can be.
func nativeBlocker(session Session, target string, opts PlanOptions) string {
	switch {
	case opts.ForceNew:
		return ReasonRequested
	case target != session.Harness:
		return ReasonOtherRuntime
	case opts.FromRuntimeLog:
		return ReasonFromRuntimeLog
	}
	command, _ := commandFor(target)
	if command.CwdScopedResume && opts.Dir != "" && opts.Dir != session.Directory {
		return ReasonNotResumable
	}
	if command.Resume == nil {
		return ReasonNotResumable
	}
	if _, ok := command.Resume(session); !ok {
		return ReasonNotResumable
	}
	if command.ResumesInSessionDir && opts.Dir != "" && !sameDirectory(opts.Dir, session.Directory) {
		return ReasonOtherDirectory
	}
	if session.SourcePath == "" {
		return ReasonSessionGone
	}
	if _, err := os.Stat(session.SourcePath); err != nil {
		return ReasonSessionGone
	}
	return ""
}

// resumeDir is the directory the runtime starts in. A runtime reopens and scopes a session by its
// directory, so a session whose directory is gone does not start anywhere else by accident.
func resumeDir(session Session, override string) (string, error) {
	dir := strings.TrimSpace(override)
	if dir == "" {
		dir = session.Directory
	}
	if dir == "" {
		return "", fmt.Errorf("%s session %s has no recorded directory; pass --cwd", session.Harness, session.ID)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("directory %s does not exist on this machine; pass --cwd to start somewhere else", dir)
	}
	return dir, nil
}

// NewSessionPrompt is the first message of a session started from a brief. It names the brief by
// path rather than inlining it: the brief carries prompt text and command output, and a command
// line is visible to every local user through the process table.
//
// The prompt ends with the handoff marker, so whatever records the new session's first prompt can
// link it to the session it continues (a session.handoff event).
func NewSessionPrompt(session Session, briefPath string) string {
	prompt := fmt.Sprintf("Continue the work from an earlier %s session. Beacon wrote a handoff brief of it at %s. "+
		"Read that file first, then tell me what you understand the task to be and wait for me to confirm before changing anything.",
		RuntimeLabel(session.Harness), briefPath)
	if marker := asymptoteobserve.HandoffMarker(session.Harness, session.ID); marker != "" {
		prompt += "\n\n" + marker
	}
	return prompt
}

// ReasonText explains a new-session reason to a person.
func ReasonText(reason string) string {
	switch reason {
	case ReasonRequested:
		return "a new session was requested"
	case ReasonOtherRuntime:
		return "it continues in a different runtime"
	case ReasonNotResumable:
		return "this runtime's CLI cannot reopen this kind of session"
	case ReasonSessionGone:
		return "the session file is no longer on this machine"
	case ReasonFromRuntimeLog:
		return "the session is known only from Beacon's runtime log"
	case ReasonOtherDirectory:
		return "its runtime reopens it only from the directory it ran in"
	}
	return reason
}

// StartableNames names the runtimes Beacon can start, for messages.
func StartableNames() []string {
	var names []string
	for i, r := range runtimes {
		if r.Command != nil {
			names = append(names, RuntimeNames()[i])
		}
	}
	return names
}

// sameDirectory reports whether a and b name one directory, symlinks resolved.
func sameDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, rb := resolveExisting(a), resolveExisting(b)
	if ra == "" || rb == "" {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
