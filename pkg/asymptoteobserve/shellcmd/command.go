package shellcmd

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Unwrapped returns the command's words after the wrappers that run the real command in the same
// directory, with the same standard input: command, builtin, exec, nohup, time, setsid and
// chronic; sudo and env with their options and NAME=value words; timeout, nice, ionice, stdbuf,
// doas, torsocks, proxychains and caffeinate with their options (and timeout's duration); and
// the runners that start a command in a project's environment: `uv run`, `poetry run`,
// `pipenv run`, `bundle exec` and `mise exec ... --`. A wrapper is recognized by its base name,
// so /usr/bin/env, ~/bin/sudo and $PREFIX/bin/timeout unwrap like env, sudo and timeout. ok is
// false, with no words, when a wrapper option moves or replaces the command, runs none, or makes
// the wrapper act on something else (sudo -i/-D/-R/-l, env -C/-S, doas -s, ionice -p, command
// -v, uv run --directory/--module, and their long forms). It reads past a PowerShell
// assignment's `$name =` when a bare name follows it ($t = az account get-access-token), which
// bash would read as running the variable's value and no command line means; a quoted string or
// a number there is a value, not a command.
func (c Command) Unwrapped() (words []Word, ok bool) {
	words = c.Argv
	if len(words) > 2 && isPowerShellVariable(words[0]) && words[1].Known && words[1].Text == "=" &&
		words[2].Known && words[2].Raw == words[2].Text && words[2].Text != "" && !unicode.IsDigit(rune(words[2].Text[0])) {
		words = words[2:]
	}
	for len(words) > 0 {
		spec, skip := wrapperOf(words)
		if spec == nil {
			return words, true
		}
		if words, ok = afterOptions(words[skip:], spec); !ok {
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

// wrapperOf returns the wrapper the first of words names, and how many words name it (two for
// `uv run`), or nil when it is no wrapper.
func wrapperOf(words []Word) (spec *wrapperSpec, skip int) {
	sub := func(names ...string) bool {
		return len(words) > 1 && words[1].Known && slices.Contains(names, words[1].Text)
	}
	switch commandBase(words[0]) {
	case "builtin", "nohup", "setsid", "chronic":
		return &wrapperPlain, 1
	case "command":
		return &wrapperCommand, 1
	case "exec":
		return &wrapperExec, 1
	case "time":
		return &wrapperTime, 1
	case "sudo":
		return &wrapperSudo, 1
	case "env":
		return &wrapperEnv, 1
	case "timeout":
		return &wrapperTimeout, 1
	case "nice":
		return &wrapperNice, 1
	case "ionice":
		return &wrapperIonice, 1
	case "stdbuf":
		return &wrapperStdbuf, 1
	case "doas":
		return &wrapperDoas, 1
	case "torsocks":
		return &wrapperTorsocks, 1
	case "proxychains", "proxychains4":
		return &wrapperProxychains, 1
	case "caffeinate":
		return &wrapperCaffeinate, 1
	case "uv":
		if sub("run") {
			return &wrapperUvRun, 2
		}
	case "poetry", "pipenv":
		if sub("run") {
			return &wrapperPlain, 2
		}
	case "bundle":
		if sub("exec") {
			return &wrapperPlain, 2
		}
	case "mise":
		// mise exec [tool@version].. -- command: the command is what follows the --. With -c
		// it is a string, and -C runs it somewhere else.
		if sub("exec", "x") {
			for i := 2; i < len(words) && words[i].Known; i++ {
				switch t := words[i].Text; {
				case t == "--":
					return &wrapperPlain, i + 1
				case t == "-c" || t == "-C" || strings.HasPrefix(t, "--command") || strings.HasPrefix(t, "--cd"):
					return nil, 0
				}
			}
		}
	}
	return nil, 0
}

// commandBase is the program name a command word names (ProgramName): rm for /bin/rm, ssh-add
// for ssh-add.exe. A word whose value is unknown still names one when what follows its last
// slash is written out, as in ~/bin/tool, $HOME/go/bin/tool and "$dir"/tool: whatever the
// directory is, the program is tool. It is "" when there is none.
func commandBase(w Word) string {
	if w.Known {
		if w.Text == "" {
			return ""
		}
		return ProgramName(w.Text)
	}
	i := strings.LastIndexByte(w.Unquoted, '/')
	if i < 0 || i+1 == len(w.Unquoted) {
		return ""
	}
	base := w.Unquoted[i+1:]
	for _, r := range base {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._+-@", r) {
			return ""
		}
	}
	return withoutWindowsSuffix(base)
}

// ProgramName is the name a word runs a program by: its base name, as filepath.Base reads it on
// this host, without the .exe, .cmd or .bat of a Windows executable, so ssh-add.exe and
// C:\Windows\System32\curl.exe are ssh-add and curl.
func ProgramName(word string) string {
	return withoutWindowsSuffix(filepath.Base(word))
}

func withoutWindowsSuffix(base string) string {
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		switch strings.ToLower(base[dot:]) {
		case ".exe", ".cmd", ".bat":
			return base[:dot]
		}
	}
	return base
}

// isPowerShellVariable reports whether w is a PowerShell variable alone: $t, $env:TOKEN.
func isPowerShellVariable(w Word) bool {
	name, ok := strings.CutPrefix(w.Unquoted, "$")
	if w.Known || !ok || name == "" || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for _, r := range name {
		if r != '_' && r != ':' && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// run returns the base name of the unwrapped command and the words after it: cat and [.env] for
// `sudo timeout 5 cat .env`. The name is "" when there is none or it is unknown.
func (c Command) run() (name string, args []Word) {
	words, _ := c.Unwrapped()
	if len(words) == 0 {
		return "", nil
	}
	if name = commandBase(words[0]); name == "" {
		return "", nil
	}
	return name, words[1:]
}

// Name is the program name of the unwrapped command (rm for /bin/rm, tool for ~/bin/tool,
// ssh-add for ssh-add.exe), or "" when there is none or it is unknown (commandBase).
func (c Command) Name() string {
	name, _ := c.run()
	return name
}

// ArgsText returns the words after the unwrapped command's name as text, one word per line, so
// that one regular expression can ask for whole words that follow each other: "auth\ntoken" for
// `sudo gh auth token`. A word whose value is unknown is an empty line, and a newline inside a
// word is written as a space, so a line is always one whole word. It is "" when the command has
// no known name or nothing follows it.
func (c Command) ArgsText() string {
	_, args := c.run()
	if len(args) == 0 {
		return ""
	}
	size := len(args) - 1
	for _, w := range args {
		size += len(w.Text)
	}
	var b strings.Builder
	b.Grow(size)
	for i, w := range args {
		if i > 0 {
			b.WriteByte('\n')
		}
		if strings.IndexByte(w.Text, '\n') < 0 {
			b.WriteString(w.Text)
		} else {
			b.WriteString(strings.ReplaceAll(w.Text, "\n", " "))
		}
	}
	return b.String()
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
	// sudo -l lists what the user may run and -v validates: with a command after it, -l says
	// whether that command is allowed and does not run it. -e (sudoedit) edits its operands, and
	// -K, -V and -h take no command.
	wrapperSudo = wrapperSpec{
		optionSpec:   optionSpec{short: "CghpRTtUu", long: []string{"--close-from", "--group", "--host", "--prompt", "--role", "--command-timeout", "--type", "--other-user", "--user"}},
		blocking:     "DRilveKV",
		blockingLong: []string{"--chdir", "--chroot", "--login", "--list", "--validate", "--edit", "--remove-timestamp", "--version", "--help"},
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
	wrapperDoas        = wrapperSpec{optionSpec: optionSpec{short: "au"}, blocking: "sC"} // -s runs a shell, -C only checks the config
	wrapperTorsocks    = wrapperSpec{optionSpec: optionSpec{short: "aPup", long: []string{"--address", "--port", "--user", "--pass"}}}
	wrapperProxychains = wrapperSpec{optionSpec: optionSpec{short: "f"}}
	// builtin, nohup, setsid, chronic, and the two-word runners take the command as it is.
	wrapperPlain = wrapperSpec{}
	// command -v and -V say what a name is and run nothing; exec -a names the process; time
	// (the program, not the shell's keyword, which is no word of the command) writes to -o.
	wrapperCommand    = wrapperSpec{blocking: "vV"}
	wrapperExec       = wrapperSpec{optionSpec: optionSpec{short: "a"}}
	wrapperTime       = wrapperSpec{optionSpec: optionSpec{short: "of", long: []string{"--output", "--format"}}}
	wrapperCaffeinate = wrapperSpec{optionSpec: optionSpec{short: "tw"}}
	// uv run starts the command in the project's environment. --directory runs it somewhere
	// else, and --module runs a Python module, not the word after it.
	wrapperUvRun = wrapperSpec{
		optionSpec: optionSpec{short: "pwifPC", long: []string{
			"--extra", "--no-extra", "--group", "--no-group", "--only-group", "--with", "--with-editable",
			"--with-requirements", "--env-file", "--package", "--project", "--python", "--python-platform",
			"--python-preference", "--index", "--default-index", "--index-url", "--extra-index-url",
			"--find-links", "--index-strategy", "--keyring-provider", "--upgrade-package", "--resolution",
			"--prerelease", "--fork-strategy", "--exclude-newer", "--exclude-newer-package",
			"--refresh-package", "--reinstall-package", "--config-setting", "--config-settings-package",
			"--no-binary-package", "--no-build-package", "--no-build-isolation-package", "--link-mode",
			"--color", "--cache-dir", "--config-file", "--allow-insecure-host", "--torch-backend",
		}},
		blocking:     "mhV",
		blockingLong: []string{"--directory", "--module", "--help", "--version"},
	}
	// The same wrappers read for a command they run somewhere else (movedCommand): the
	// options that move it take their value or are plain flags, and the ones that run no
	// command still stop the reading.
	wrapperSudoMoved = wrapperSpec{
		optionSpec:   optionSpec{short: wrapperSudo.short + "DR", long: append(slices.Clone(wrapperSudo.long), "--chdir", "--chroot")},
		blocking:     "lveKV",
		blockingLong: []string{"--list", "--validate", "--edit", "--remove-timestamp", "--version", "--help"},
		assignments:  true,
	}
	wrapperEnvMoved = wrapperSpec{
		optionSpec:   optionSpec{short: "uC", long: []string{"--unset", "--chdir"}},
		blocking:     "S",
		blockingLong: []string{"--split-string"},
		assignments:  true,
	}
	wrapperUvRunMoved = wrapperSpec{
		optionSpec:   optionSpec{short: wrapperUvRun.short, long: append(slices.Clone(wrapperUvRun.long), "--directory")},
		blocking:     "mhV",
		blockingLong: []string{"--module", "--help", "--version"},
	}
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
