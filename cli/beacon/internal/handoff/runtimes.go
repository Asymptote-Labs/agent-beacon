package handoff

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/opencodesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pisession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/primesession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Runtime is everything handoff knows about one agent runtime. Adding a runtime is adding one entry
// to runtimes: how to name it, where its sessions are stored, and how to start its CLI.
type Runtime struct {
	// Harness is the name the endpoint event schema uses.
	Harness string
	// Label names the runtime to a person.
	Label string
	// Aliases are the extra names --harness and --agent accept, besides Harness itself.
	Aliases []string
	// NewSource builds the runtime's session source, reading its store from dir, or from the
	// runtime's default location when dir is empty. nil when Beacon reads no local store for the
	// runtime; its sessions can then still be exported from the runtime log.
	NewSource func(dir string) Source
	// Command starts the runtime's CLI. nil when Beacon cannot start the runtime.
	Command *runtimeCommand
}

// runtimeCommand is how Beacon starts one runtime's CLI. Every argument vector is checked against
// that CLI's own --help.
type runtimeCommand struct {
	Executable string
	// Resume returns the arguments that reopen session, or ok=false when this runtime's CLI cannot
	// reopen it. nil when the CLI has no way to reopen a session by id.
	Resume func(session Session) (args []string, ok bool)
	// NewSession returns the arguments that start an interactive session with prompt as its first
	// message.
	NewSession func(prompt string) []string
	// ResumesInSessionDir marks a CLI that finds a session by the directory it is started in, so
	// a native reopen from any other directory (--cwd) would not find it.
	ResumesInSessionDir bool
	// Env overrides the runtime's environment: a variable set to a value is set, one set to "" is
	// removed. It holds switches only, never credentials, because the plan prints it.
	Env map[string]string
}

// Harness names, as the endpoint event schema spells them.
const (
	HarnessClaude   = claudesession.Harness
	HarnessCodex    = codexsession.Harness
	HarnessOpenCode = opencodesession.Harness
	HarnessCline    = clinesession.Harness
	HarnessPi       = pisession.Harness
	HarnessPrime    = primesession.Harness
	HarnessGrok     = groksession.Harness
)

// runtimes lists every runtime handoff supports, in display order.
var runtimes = []Runtime{
	{
		Harness:   HarnessClaude,
		Label:     "Claude Code",
		Aliases:   []string{"claude", "claude-code"},
		NewSource: func(dir string) Source { return &claudeSource{dir: dir} },
		Command: &runtimeCommand{
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
	},
	{
		Harness:   HarnessCodex,
		Label:     "Codex CLI",
		Aliases:   []string{"codex", "codex-cli"},
		NewSource: func(dir string) Source { return &codexSource{dir: dir} },
		Command: &runtimeCommand{
			Executable: "codex",
			Resume:     func(s Session) ([]string, bool) { return []string{"resume", s.ID}, true },
			NewSession: func(prompt string) []string { return []string{prompt} },
		},
	},
	{
		Harness:   HarnessOpenCode,
		Label:     "OpenCode",
		NewSource: func(dir string) Source { return &openCodeSource{dir: dir} },
		Command: &runtimeCommand{
			Executable: "opencode",
			Resume:     func(s Session) ([]string, bool) { return []string{"--session", s.ID}, true },
			NewSession: func(prompt string) []string { return []string{"--prompt", prompt} },
		},
	},
	{
		Harness:   HarnessCline,
		Label:     "Cline",
		NewSource: func(dir string) Source { return &clineSource{dir: dir} },
		// Cline approves every tool call unless told otherwise. A session Beacon starts never gains
		// that on Beacon's say-so; the user can still turn it on inside the TUI.
		Command: &runtimeCommand{
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
	},
	{
		Harness:   HarnessPi,
		Label:     "Pi",
		Aliases:   []string{"pi", "pi-cli"},
		NewSource: func(dir string) Source { return &piSource{dir: dir} },
		Command: &runtimeCommand{
			Executable: "pi",
			// --session takes the transcript's path as well as its id; the path names it exactly,
			// wherever Pi's session directory is.
			Resume: func(s Session) ([]string, bool) { return []string{"--session", s.SourcePath}, true },
			// -- ends Pi's options, so the prompt is never read as one.
			NewSession: func(prompt string) []string { return []string{"--", prompt} },
		},
	},
	{
		Harness:   HarnessPrime,
		Label:     "Prime Agent",
		Aliases:   []string{"prime", "prime-agent"},
		NewSource: func(dir string) Source { return &primeSource{dir: dir} },
		Command: &runtimeCommand{
			Executable: "prime-agent",
			Resume: func(s Session) ([]string, bool) {
				// A subagent transcript belongs to the session that spawned it, not to one the CLI
				// reopens on its own.
				if s.Subagent {
					return nil, false
				}
				return []string{"--resume", s.SourcePath}, true
			},
			NewSession: func(prompt string) []string { return []string{"--", prompt} },
		},
	},
	{
		Harness:   HarnessGrok,
		Label:     "Grok Build",
		Aliases:   []string{"grok-build"},
		NewSource: func(dir string) Source { return &grokSource{dir: dir} },
		// Grok's configured permission mode can be always-approve. A session Beacon starts or
		// reopens runs in the mode that asks, whatever the config says; the user can still change
		// the mode inside the TUI.
		Command: &runtimeCommand{
			Executable: "grok",
			Resume: func(s Session) ([]string, bool) {
				// A subagent is a child of the session that spawned it, not one a person reopens.
				if s.Subagent {
					return nil, false
				}
				// Grok looks a session up under the directory it runs in, which is the session's own.
				return []string{"--permission-mode", "default", "--resume", s.ID}, true
			},
			NewSession: func(prompt string) []string { return []string{"--permission-mode", "default", prompt} },
			// Grok looks a session id up under the directory it is started in.
			ResumesInSessionDir: true,
		},
	},
}

// Harnesses lists the runtimes handoff supports, in display order.
var Harnesses = func() []string {
	names := make([]string, 0, len(runtimes))
	for _, r := range runtimes {
		names = append(names, r.Harness)
	}
	return names
}()

// LookupRuntime returns the registry entry for harness.
func LookupRuntime(harness string) (Runtime, bool) {
	for _, r := range runtimes {
		if r.Harness == harness {
			return r, true
		}
	}
	return Runtime{}, false
}

// ParseHarness resolves a user-supplied runtime name to its harness name. An empty name stays
// empty, meaning every runtime. A name the event schema normalises to a supported harness is
// accepted too.
func ParseHarness(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", nil
	}
	for _, r := range runtimes {
		if name == r.Harness {
			return r.Harness, nil
		}
		for _, alias := range r.Aliases {
			if name == alias {
				return r.Harness, nil
			}
		}
	}
	if normalized := asymptoteobserve.NormalizeHarnessName(name); normalized != "" {
		if _, ok := LookupRuntime(normalized); ok {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("unsupported runtime %q (supported: %s)", name, strings.Join(RuntimeNames(), ", "))
}

// RuntimeNames is the shortest name each runtime answers to, for messages.
func RuntimeNames() []string {
	names := make([]string, 0, len(runtimes))
	for _, r := range runtimes {
		name := r.Harness
		for _, alias := range r.Aliases {
			if len(alias) < len(name) {
				name = alias
			}
		}
		names = append(names, name)
	}
	return names
}

// RuntimeLabel is how a brief names a runtime to a reader.
func RuntimeLabel(harness string) string {
	if r, ok := LookupRuntime(harness); ok {
		return r.Label
	}
	return harness
}

// StoreDirs overrides where runtimes' session stores are read from, keyed by harness. A runtime
// with no entry reads its store from its default location.
type StoreDirs map[string]string

// DefaultSources returns a source for every runtime whose store Beacon reads.
func DefaultSources(dirs StoreDirs) []Source {
	var sources []Source
	for _, r := range runtimes {
		if r.NewSource != nil {
			sources = append(sources, r.NewSource(dirs[r.Harness]))
		}
	}
	return sources
}

func commandFor(harness string) (*runtimeCommand, bool) {
	r, ok := LookupRuntime(harness)
	if !ok || r.Command == nil {
		return nil, false
	}
	return r.Command, true
}
