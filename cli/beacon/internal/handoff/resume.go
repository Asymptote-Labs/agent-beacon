package handoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
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
)

// runtimeCommand is how Beacon starts one runtime's CLI. Every argument vector was checked against
// that CLI's own --help.
type runtimeCommand struct {
	Executable string
	// Resume returns the arguments that reopen session, or ok=false when this runtime's CLI cannot
	// reopen it.
	Resume func(session Session) (args []string, ok bool)
	// NewSession returns the arguments that start an interactive session with prompt as its first
	// message.
	NewSession func(prompt string) []string
}

var runtimeCommands = map[string]runtimeCommand{
	HarnessClaude: {
		Executable: "claude",
		Resume: func(s Session) ([]string, bool) {
			// A subagent transcript is a sidechain of its parent; `claude --resume` reopens
			// top-level sessions only.
			if s.Subagent {
				return nil, false
			}
			return []string{"--resume", s.ID}, true
		},
		NewSession: func(prompt string) []string { return []string{prompt} },
	},
	HarnessCodex: {
		Executable: "codex",
		Resume:     func(s Session) ([]string, bool) { return []string{"resume", s.ID}, true },
		NewSession: func(prompt string) []string { return []string{prompt} },
	},
	HarnessOpenCode: {
		Executable: "opencode",
		Resume:     func(s Session) ([]string, bool) { return []string{"--session", s.ID}, true },
		NewSession: func(prompt string) []string { return []string{"--prompt", prompt} },
	},
	HarnessCline: {
		// Cline approves every tool call unless told otherwise. A session Beacon starts never
		// gains that on Beacon's say-so; the user can still turn it on inside the TUI.
		Executable: "cline",
		Resume: func(s Session) ([]string, bool) {
			// `cline --id` reopens CLI sessions; the older task history and subagent threads are
			// not sessions it can load.
			if s.Store != clinesession.SourceMessages || s.Subagent {
				return nil, false
			}
			return []string{"--tui", "--auto-approve", "false", "--id", s.ID}, true
		},
		NewSession: func(prompt string) []string {
			return []string{"--tui", "--auto-approve", "false", prompt}
		},
	},
}

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
	targetCommand, ok := runtimeCommands[target]
	if !ok {
		return Plan{}, fmt.Errorf("%s sessions cannot be started by beacon handoff (supported: claude, codex, opencode, cline)", target)
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
	plan.Args = targetCommand.NewSession(NewSessionPrompt(session, opts.BriefPath))
	return plan, nil
}

func nativeArgs(command runtimeCommand, session Session) []string {
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
	if _, ok := runtimeCommands[target].Resume(session); !ok {
		return ReasonNotResumable
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
func NewSessionPrompt(session Session, briefPath string) string {
	return fmt.Sprintf("Continue the work from an earlier %s session. Beacon wrote a handoff brief of it at %s. "+
		"Read that file first, then tell me what you understand the task to be and wait for me to confirm before changing anything.",
		RuntimeLabel(session.Harness), briefPath)
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
	}
	return reason
}
