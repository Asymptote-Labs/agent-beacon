package handoff

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/openclawsession"
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
	HarnessOpenClaw = openclawsession.Harness

	// Runtimes Beacon starts but whose sessions it reads only from the runtime log.
	HarnessOhMyPi      = "omp"
	HarnessGemini      = "gemini_cli"
	HarnessQwen        = "qwen_code"
	HarnessKiro        = "kiro"
	HarnessAntigravity = "antigravity_cli"
	HarnessDevin       = "devin-cli"
	HarnessMuse        = "muse_code"
	HarnessOpenHands   = "openhands"
	HarnessGoose       = "goose"
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
		Harness:   HarnessOpenClaw,
		Label:     "OpenClaw Gateway",
		Aliases:   []string{"openclaw", "openclaw-gateway"},
		NewSource: func(dir string) Source { return &openClawSource{dir: dir} },
		// The TUI is a client of the Gateway: it needs the Gateway running, and a conversation lives
		// on the Gateway under its session key, not its transcript id. --deliver stays off, so
		// replies are not sent out through the Gateway's chat channels.
		Command: &runtimeCommand{
			Executable: "openclaw",
			Resume: func(s Session) ([]string, bool) {
				if s.Key == "" {
					return nil, false
				}
				return []string{"tui", "--session", s.Key}, true
			},
			NewSession: func(prompt string) []string {
				return []string{"tui", "--session", openClawNewSessionKey(prompt), "--message", prompt}
			},
		},
	},

	// The runtimes below keep no session store Beacon reads. Their sessions come from Beacon's
	// runtime log, so they always continue from a brief; each can also be the runtime another
	// session continues in. Where a CLI lets its approval mode be set, the command pins the mode
	// that asks, so a user or project setting that approves everything does not carry over to a
	// session Beacon starts.
	{
		Harness: HarnessOhMyPi,
		Label:   "Oh My Pi",
		Aliases: []string{"oh-my-pi"},
		// Oh My Pi approves every tool call by default (tools.approvalMode "yolo").
		Command: &runtimeCommand{
			Executable: "omp",
			NewSession: func(prompt string) []string { return []string{"--approval-mode=always-ask", prompt} },
		},
	},
	{
		Harness: HarnessGemini,
		Label:   "Gemini CLI",
		Aliases: []string{"gemini", "gemini-cli"},
		Command: &runtimeCommand{
			Executable: "gemini",
			NewSession: func(prompt string) []string {
				return []string{"--approval-mode", "default", "--prompt-interactive", prompt}
			},
		},
	},
	{
		Harness: HarnessQwen,
		Label:   "Qwen Code",
		Aliases: []string{"qwen", "qwen-code"},
		// A positional prompt is one-shot in Qwen Code; --prompt-interactive keeps the session open.
		Command: &runtimeCommand{
			Executable: "qwen",
			NewSession: func(prompt string) []string {
				return []string{"--approval-mode", "default", "--prompt-interactive", prompt}
			},
		},
	},
	{
		Harness: HarnessKiro,
		Label:   "Kiro",
		Aliases: []string{"kiro-cli"},
		Command: &runtimeCommand{
			Executable: "kiro-cli",
			NewSession: func(prompt string) []string { return []string{"chat", prompt} },
		},
	},
	{
		Harness: HarnessAntigravity,
		Label:   "Antigravity CLI",
		Aliases: []string{"antigravity", "agy"},
		Command: &runtimeCommand{
			Executable: "agy",
			NewSession: func(prompt string) []string { return []string{"--prompt-interactive", prompt} },
		},
	},
	{
		Harness: HarnessDevin,
		Label:   "Devin CLI",
		// Beacon's Devin CLI hooks record "devin-cli"; hooks written by older versions recorded
		// "devin", which reaches this entry through its alias.
		Aliases: []string{"devin"},
		// DEVIN_PERMISSION_MODE can hold "dangerous", which approves every tool; the flag wins over
		// it. Devin reads a bare argument as a path to open, so the prompt follows --.
		Command: &runtimeCommand{
			Executable: "devin",
			NewSession: func(prompt string) []string { return []string{"--permission-mode", "auto", "--", prompt} },
		},
	},
	{
		Harness: HarnessMuse,
		Label:   "Muse Code",
		Aliases: []string{"muse", "muse-code"},
		Command: &runtimeCommand{
			Executable: "muse",
			NewSession: func(prompt string) []string { return []string{"--approval-mode", "on-request", prompt} },
		},
	},
	{
		Harness: HarnessOpenHands,
		Label:   "OpenHands",
		Command: &runtimeCommand{
			Executable: "openhands",
			NewSession: func(prompt string) []string { return []string{"--task", prompt} },
		},
	},
	{
		Harness: HarnessGoose,
		Label:   "goose",
		// goose approves every tool call by default and has no flag for its mode, only GOOSE_MODE.
		Command: &runtimeCommand{
			Executable: "goose",
			NewSession: func(prompt string) []string { return []string{"run", "--interactive", "--text", prompt} },
			Env:        map[string]string{"GOOSE_MODE": "approve"},
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

// ReadsStore reports whether Beacon reads harness's own session store.
func ReadsStore(harness string) bool {
	r, ok := LookupRuntime(harness)
	return ok && r.NewSource != nil
}

// canonicalHarness is the registry's harness for a name a log row carries: the name itself, or the
// runtime it is an alias of. A name no runtime answers to is returned as it is.
func canonicalHarness(name string) string {
	if _, ok := LookupRuntime(name); ok {
		return name
	}
	for _, r := range runtimes {
		for _, alias := range r.Aliases {
			if name == alias {
				return r.Harness
			}
		}
	}
	return name
}

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
