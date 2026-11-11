package shellcmd

import (
	"path/filepath"
	"slices"
	"strings"
)

// Unwrapped returns the command's words after the wrappers that run the real command in the same
// directory: command, builtin, exec, nohup and time, and sudo and env with their options and
// NAME=value words. A wrapper is recognized by its base name, so /usr/bin/env and /usr/bin/sudo
// unwrap like env and sudo. ok is false, with no words, when a wrapper option moves or replaces
// the command (sudo -i/-D/-R, env -C/-S and their long forms). It stops at the first unknown word.
func (c Command) Unwrapped() (words []Word, ok bool) {
	return unwrap(c.Argv, false)
}

// unwrap returns words after the wrappers Unwrapped skips and, with runners, after the ones that
// run the command under a limit, a priority or a proxy as well (runnerWrappers).
func unwrap(words []Word, runners bool) (_ []Word, ok bool) {
	for len(words) > 0 && words[0].Known {
		switch name := filepath.Base(words[0].Text); name {
		case "command", "builtin", "exec", "nohup", "time":
			words = words[1:]
		case "sudo":
			if words, ok = afterOptions(words[1:], wrapperSudo); !ok {
				return nil, false
			}
			words = afterAssignments(words)
		case "env":
			if words, ok = afterOptions(words[1:], wrapperEnv); !ok {
				return nil, false
			}
			words = afterAssignments(words)
		default:
			if !runners {
				return words, true
			}
			runner, isRunner := runnerWrappers[name]
			if !isRunner {
				return words, true
			}
			if words, ok = afterOptions(words[1:], runner.wrapperSpec); !ok {
				return nil, false
			}
			if len(words) < runner.operands {
				return nil, true
			}
			words = words[runner.operands:]
		}
	}
	return words, true
}

// run returns the base name of the command the statement runs and the words after it, looking
// through the wrappers Unwrapped skips and the runner wrappers too: cat and [.env] for
// `sudo timeout 5 cat .env`. The name is "" when there is none or it is unknown.
func (c Command) run() (name string, args []Word) {
	words, _ := unwrap(c.Argv, true)
	if len(words) == 0 || !words[0].Known || words[0].Text == "" {
		return "", nil
	}
	return filepath.Base(words[0].Text), words[1:]
}

// Name is the base name of the unwrapped command (rm for /bin/rm), or "" when there is none or
// it is unknown. The base name is the host's, as filepath.Base reads it.
func (c Command) Name() string {
	words, _ := c.Unwrapped()
	if len(words) == 0 || !words[0].Known || words[0].Text == "" {
		return ""
	}
	return filepath.Base(words[0].Text)
}

// Is reports whether the unwrapped command is name, followed by the known words sub
// (Is("git", "mv") for git mv a b).
func (c Command) Is(name string, sub ...string) bool {
	if c.Name() != name {
		return false
	}
	words, _ := c.Unwrapped()
	if len(words) < 1+len(sub) {
		return false
	}
	for i, s := range sub {
		if w := words[1+i]; !w.Known || w.Text != s {
			return false
		}
	}
	return true
}

type wrapperSpec struct {
	optionSpec
	blocking     string   // short options that move or replace the command
	blockingLong []string // long forms of the same
}

// runnerWrapper is a wrapper that runs its command unchanged, after its options and a number of
// operands of its own (timeout's duration).
type runnerWrapper struct {
	wrapperSpec
	operands int
}

var (
	wrapperSudo = wrapperSpec{
		optionSpec:   optionSpec{short: "CghpRTtUu", long: []string{"--close-from", "--group", "--host", "--prompt", "--role", "--command-timeout", "--type", "--other-user", "--user"}},
		blocking:     "DRi",
		blockingLong: []string{"--chdir", "--chroot", "--login"},
	}
	wrapperEnv = wrapperSpec{
		optionSpec:   optionSpec{short: "u", long: []string{"--unset"}},
		blocking:     "CS",
		blockingLong: []string{"--chdir", "--split-string"},
	}

	// runnerWrappers run a command under a limit, a priority, a buffering mode, another account
	// or a proxy. Name and Unwrapped stop at them, as they always have; FileReads looks through
	// them, so `timeout 5 cat .env` reads .env. An option that makes one act on something other
	// than a command it runs (ionice -p, doas -s) hides the command.
	runnerWrappers = map[string]runnerWrapper{
		"timeout": {wrapperSpec: wrapperSpec{optionSpec: optionSpec{short: "ks", long: []string{"--kill-after", "--signal"}}}, operands: 1},
		"nice":    {wrapperSpec: wrapperSpec{optionSpec: optionSpec{short: "n", long: []string{"--adjustment"}}}},
		"ionice": {wrapperSpec: wrapperSpec{
			optionSpec:   optionSpec{short: "cn", long: []string{"--class", "--classdata"}},
			blocking:     "pPu",
			blockingLong: []string{"--pid", "--pgid", "--uid"},
		}},
		"stdbuf": {wrapperSpec: wrapperSpec{optionSpec: optionSpec{short: "ioe", long: []string{"--input", "--output", "--error"}}}},
		"doas": {wrapperSpec: wrapperSpec{
			optionSpec: optionSpec{short: "uC"},
			blocking:   "s",
		}},
		"torsocks":     {wrapperSpec: wrapperSpec{optionSpec: optionSpec{short: "aPup", long: []string{"--address", "--port", "--user", "--pass"}}}},
		"proxychains":  {wrapperSpec: wrapperSpec{optionSpec: optionSpec{short: "f"}}},
		"proxychains4": {wrapperSpec: wrapperSpec{optionSpec: optionSpec{short: "f"}}},
	}
)

// afterOptions returns the words after a wrapper's own options. ok is false when one of them
// moves or replaces the command.
func afterOptions(words []Word, spec wrapperSpec) ([]Word, bool) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case !w.Known || w.Text == "-" || !strings.HasPrefix(w.Text, "-"):
			return words[i:], true
		case w.Text == "--":
			return words[i+1:], true
		case strings.HasPrefix(w.Text, "--"):
			name, _, attached := strings.Cut(w.Text, "=")
			if slices.Contains(spec.blockingLong, name) {
				return nil, false
			}
			if slices.Contains(spec.long, name) && !attached {
				i++
			}
		default:
			for j := 1; j < len(w.Text); j++ {
				if strings.IndexByte(spec.blocking, w.Text[j]) >= 0 {
					return nil, false
				}
				if strings.IndexByte(spec.short, w.Text[j]) >= 0 {
					if j+1 == len(w.Text) {
						i++
					}
					break
				}
			}
		}
	}
	return nil, true
}

func afterAssignments(words []Word) []Word {
	for len(words) > 0 && words[0].Known {
		name, _, ok := strings.Cut(words[0].Text, "=")
		if !ok || !isShellName(name) {
			break
		}
		words = words[1:]
	}
	return words
}

func isShellName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
