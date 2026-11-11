package shellcmd

import (
	"path/filepath"
	"slices"
	"strings"
)

// Unwrapped returns the command's words after the wrappers that run the real command in the same
// directory: command, builtin, exec, nohup and time; sudo and env with their options and
// NAME=value words; and timeout, nice, ionice, stdbuf, doas, torsocks and proxychains with their
// options (and timeout's duration). A wrapper is recognized by its base name, so /usr/bin/env and
// /usr/bin/sudo unwrap like env and sudo. ok is false, with no words, when a wrapper option moves
// or replaces the command, or makes the wrapper act on something else (sudo -i/-D/-R, env -C/-S,
// doas -s, ionice -p, and their long forms). It stops at the first unknown word.
func (c Command) Unwrapped() (words []Word, ok bool) {
	words = c.Argv
	for len(words) > 0 && words[0].Known {
		var spec *wrapperSpec
		switch filepath.Base(words[0].Text) {
		case "command", "builtin", "exec", "nohup", "time":
			words = words[1:]
			continue
		case "sudo":
			spec = &wrapperSudo
		case "env":
			spec = &wrapperEnv
		case "timeout":
			spec = &wrapperTimeout
		case "nice":
			spec = &wrapperNice
		case "ionice":
			spec = &wrapperIonice
		case "stdbuf":
			spec = &wrapperStdbuf
		case "doas":
			spec = &wrapperDoas
		case "torsocks":
			spec = &wrapperTorsocks
		case "proxychains", "proxychains4":
			spec = &wrapperProxychains
		default:
			return words, true
		}
		if words, ok = afterOptions(words[1:], spec); !ok {
			return nil, false
		}
		if spec.assignments {
			words = afterAssignments(words)
		}
		if len(words) < spec.operands {
			return nil, true
		}
		words = words[spec.operands:]
	}
	return words, true
}

// run returns the base name of the unwrapped command and the words after it: cat and [.env] for
// `sudo timeout 5 cat .env`. The name is "" when there is none or it is unknown.
func (c Command) run() (name string, args []Word) {
	words, _ := c.Unwrapped()
	if len(words) == 0 || !words[0].Known || words[0].Text == "" {
		return "", nil
	}
	return filepath.Base(words[0].Text), words[1:]
}

// Name is the base name of the unwrapped command (rm for /bin/rm), or "" when there is none or
// it is unknown. The base name is the host's, as filepath.Base reads it.
func (c Command) Name() string {
	name, _ := c.run()
	return name
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

// wrapperSpec is a wrapper that runs its command unchanged, in the same directory, after its own
// options.
type wrapperSpec struct {
	optionSpec
	// blocking are the short options that move or replace the command, or make the wrapper act
	// on something other than a command it runs (ionice -p, doas -s); blockingLong are the long
	// forms of the same.
	blocking     string
	blockingLong []string
	// assignments is set when NAME=value words may follow the options (sudo, env), and operands
	// is how many words of its own the wrapper takes before the command (timeout's duration).
	assignments bool
	operands    int
}

var (
	wrapperSudo = wrapperSpec{
		optionSpec:   optionSpec{short: "CghpRTtUu", long: []string{"--close-from", "--group", "--host", "--prompt", "--role", "--command-timeout", "--type", "--other-user", "--user"}},
		blocking:     "DRi",
		blockingLong: []string{"--chdir", "--chroot", "--login"},
		assignments:  true,
	}
	wrapperEnv = wrapperSpec{
		optionSpec:   optionSpec{short: "u", long: []string{"--unset"}},
		blocking:     "CS",
		blockingLong: []string{"--chdir", "--split-string"},
		assignments:  true,
	}
	// The rest run a command under a limit, a priority, a buffering mode, another account or a
	// proxy.
	wrapperTimeout = wrapperSpec{optionSpec: optionSpec{short: "ks", long: []string{"--kill-after", "--signal"}}, operands: 1}
	wrapperNice    = wrapperSpec{optionSpec: optionSpec{short: "n", long: []string{"--adjustment"}}}
	wrapperIonice  = wrapperSpec{
		optionSpec:   optionSpec{short: "cn", long: []string{"--class", "--classdata"}},
		blocking:     "pPu",
		blockingLong: []string{"--pid", "--pgid", "--uid"},
	}
	wrapperStdbuf      = wrapperSpec{optionSpec: optionSpec{short: "ioe", long: []string{"--input", "--output", "--error"}}}
	wrapperDoas        = wrapperSpec{optionSpec: optionSpec{short: "uC"}, blocking: "s"}
	wrapperTorsocks    = wrapperSpec{optionSpec: optionSpec{short: "aPup", long: []string{"--address", "--port", "--user", "--pass"}}}
	wrapperProxychains = wrapperSpec{optionSpec: optionSpec{short: "f"}}
)

// afterOptions returns the words after a wrapper's own options. ok is false when one of them
// moves or replaces the command.
func afterOptions(words []Word, spec *wrapperSpec) ([]Word, bool) {
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
