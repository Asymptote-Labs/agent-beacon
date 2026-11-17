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
// false, with no words, when a wrapper option runs the command somewhere else (sudo -i/-D/-R,
// env -C, uv run --directory: Carried reads it), runs none (sudo -l/-v, command -v), or makes
// the wrapper act on something other than a command (sudo -e, env -S, doas -s, ionice -p,
// uv run --module), and their long forms.
func (c Command) Unwrapped() (words []Word, ok bool) {
	if c.unwrapped.how != ran {
		return nil, false
	}
	return c.Argv[c.unwrapped.at:], true
}

// outcome is what a command's wrappers do with the command after them.
type outcome uint8

const (
	// ran: the words after the wrappers run here, as the command (or there are none).
	ran outcome = iota
	// moved: a wrapper option runs them somewhere else: after a login, in another directory
	// or under another root.
	moved
	// runsNothing: a wrapper option makes the wrapper run no command (sudo -l, command -v).
	runsNothing
	// replaced: a wrapper option makes it act on something other than a command it runs
	// (sudo -e edits files, env -S splits a string, ionice -p sets another process).
	replaced
)

// unwrapped is a command as its wrappers leave it, worked out once when the command is read
// (resolve): every fact that asks what a command is asks this.
type unwrapped struct {
	// at is the index in Argv of the first word after the wrappers: the command's name when how
	// is ran, the command run elsewhere when it is moved.
	at  int
	how outcome
	// name is the base name of the word at at when how is ran (commandBase), and program the
	// program it names (programOf): "gtar" and "tar". Both are "" when there is none.
	name, program string
}

// resolve works out c.unwrapped from c.Argv.
func (c *Command) resolve() {
	at, how := unwrap(c.Argv)
	c.unwrapped = unwrapped{at: at, how: how}
	if how == ran && at < len(c.Argv) {
		c.unwrapped.name = commandBase(c.Argv[at])
		c.unwrapped.program = programOf(c.unwrapped.name)
	}
}

// unwrap walks the wrappers at the start of words: the index of the first word after them, and
// what they do with it. It reads past a PowerShell assignment's `$name =` first when a bare name
// follows it ($t = az account get-access-token), which bash would read as running the variable's
// value and no command line means; a quoted string or a number there is a value, not a command.
func unwrap(words []Word) (int, outcome) {
	at := 0
	if len(words) > 2 && isPowerShellVariable(words[0]) && words[1].Known && words[1].Text == "=" &&
		words[2].Known && words[2].Raw == words[2].Text && words[2].Text != "" && !unicode.IsDigit(rune(words[2].Text[0])) {
		at = 2
	}
	for at < len(words) {
		spec, skip := wrapperOf(words[at:])
		if spec == nil {
			break
		}
		at += skip
		n, how := spec.options(words[at:])
		at += n
		switch how {
		case runsNothing, replaced:
			return at, how
		}
		if spec.assignments {
			at += assignments(words[at:])
		}
		if how == moved {
			return at, moved
		}
		if len(words)-at < spec.operands {
			return len(words), ran
		}
		at += spec.operands
	}
	return at, ran
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

// wrapperOf returns the wrapper the first of words names, and how many words name it (two for
// `uv run`), or nil when it is no wrapper.
func wrapperOf(words []Word) (spec *wrapperSpec, skip int) {
	sub := func(names ...string) bool {
		return len(words) > 1 && words[1].Known && slices.Contains(names, words[1].Text)
	}
	switch programOf(commandBase(words[0])) {
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
	case "proxychains":
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

// otherNames are programs installed under another name than the one this package reads them
// by: GNU tools that Homebrew prefixes with g, the BSD and the other awks, the old names of
// grep -E and -F. Every fact asks a command's program (programOf), so a name added here is read
// as its program everywhere; Name stays the name as written.
var otherNames = map[string]string{
	"gtar": "tar", "bsdtar": "tar",
	"gsed": "sed",
	"gawk": "awk", "mawk": "awk", "nawk": "awk",
	"egrep": "grep", "fgrep": "grep", "ggrep": "grep",
	"gxargs": "xargs", "gfind": "find",
	"proxychains4": "proxychains",
	"nodejs":       "node",
}

// programOf is the program a base name names: tar for gtar, the name itself for most.
func programOf(name string) string {
	if program, ok := otherNames[name]; ok {
		return program
	}
	return name
}

// program returns the program the unwrapped command runs (programOf its name) and the words
// after its name: cat and [.env] for `sudo timeout 5 cat .env`, tar for gtar. It is "" and no
// words when there is none or it is unknown.
func (c *Command) program() (program string, args []Word) {
	if c.unwrapped.name == "" {
		return "", nil
	}
	return c.unwrapped.program, c.Argv[c.unwrapped.at+1:]
}

// Name is the program name of the unwrapped command (rm for /bin/rm, tool for ~/bin/tool,
// ssh-add for ssh-add.exe), or "" when there is none or it is unknown (commandBase).
func (c Command) Name() string {
	return c.unwrapped.name
}

// ArgsText returns the words after the unwrapped command's name as text, one word per line, so
// that one regular expression can ask for whole words that follow each other: "auth\ntoken" for
// `sudo gh auth token`. A word whose value is unknown is an empty line, and a newline inside a
// word is written as a space, so a line is always one whole word. It is "" when the command has
// no known name or nothing follows it.
func (c Command) ArgsText() string {
	_, args := c.program()
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
	if c.unwrapped.name != name {
		return false
	}
	_, args := c.program()
	return isSub(args, sub...)
}

// isSub reports whether args begin with the known words sub.
func isSub(args []Word, sub ...string) bool {
	if len(args) < len(sub) {
		return false
	}
	for i, s := range sub {
		if w := args[i]; !w.Known || w.Text != s {
			return false
		}
	}
	return true
}

// wrapperSpec is a wrapper that runs its command unchanged, in the same directory, after its own
// options, unless one of them says otherwise: moves, runsNothing and replaced name those, each
// with the outcome of that name. An option of one of them that is also among the options that
// take a value takes its value (sudo -D dir).
type wrapperSpec struct {
	optionSpec
	moves, runsNothing, replaced optionSpec
	// assignments is set when NAME=value words may follow the options (sudo, env), and operands
	// is how many words of its own the wrapper takes before the command (timeout's duration).
	assignments bool
	operands    int
}

var (
	// sudo -i, -D and -R run the command after a login, in another directory, under another
	// root. -l lists what the user may run and -v validates: with a command after it, -l says
	// whether that command is allowed and does not run it. -e (sudoedit) edits its operands,
	// and -K, -V and -h take no command.
	wrapperSudo = wrapperSpec{
		optionSpec: optionSpec{short: "CDghpRTtUu", long: []string{
			"--chdir", "--chroot", "--close-from", "--group", "--host", "--prompt", "--role",
			"--command-timeout", "--type", "--other-user", "--user",
		}},
		moves:       optionSpec{short: "DRi", long: []string{"--chdir", "--chroot", "--login"}},
		runsNothing: optionSpec{short: "lvKV", long: []string{"--list", "--validate", "--remove-timestamp", "--version", "--help"}},
		replaced:    optionSpec{short: "e", long: []string{"--edit"}},
		assignments: true,
	}
	wrapperEnv = wrapperSpec{
		optionSpec:  optionSpec{short: "Cu", long: []string{"--chdir", "--unset"}},
		moves:       optionSpec{short: "C", long: []string{"--chdir"}},
		replaced:    optionSpec{short: "S", long: []string{"--split-string"}},
		assignments: true,
	}
	// The rest run a command under a limit, a priority, a buffering mode, another account or a
	// proxy.
	wrapperTimeout = wrapperSpec{optionSpec: optionSpec{short: "ks", long: []string{"--kill-after", "--signal"}}, operands: 1}
	wrapperNice    = wrapperSpec{optionSpec: optionSpec{short: "n", long: []string{"--adjustment"}}}
	wrapperIonice  = wrapperSpec{
		optionSpec: optionSpec{short: "cn", long: []string{"--class", "--classdata"}},
		replaced:   optionSpec{short: "pPu", long: []string{"--pid", "--pgid", "--uid"}},
	}
	wrapperStdbuf      = wrapperSpec{optionSpec: optionSpec{short: "ioe", long: []string{"--input", "--output", "--error"}}}
	wrapperDoas        = wrapperSpec{optionSpec: optionSpec{short: "au"}, replaced: optionSpec{short: "sC"}} // -s runs a shell, -C only checks the config
	wrapperTorsocks    = wrapperSpec{optionSpec: optionSpec{short: "aPup", long: []string{"--address", "--port", "--user", "--pass"}}}
	wrapperProxychains = wrapperSpec{optionSpec: optionSpec{short: "f"}}
	// builtin, nohup, setsid, chronic, and the two-word runners take the command as it is.
	wrapperPlain = wrapperSpec{}
	// command -v and -V say what a name is and run nothing; exec -a names the process; time
	// (the program, not the shell's keyword, which is no word of the command) writes to -o.
	wrapperCommand    = wrapperSpec{runsNothing: optionSpec{short: "vV"}}
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
			"--directory",
		}},
		moves:       optionSpec{long: []string{"--directory"}},
		runsNothing: optionSpec{short: "hV", long: []string{"--help", "--version"}},
		replaced:    optionSpec{short: "m", long: []string{"--module"}},
	}
)

// options reads the wrapper's own options at the start of words: how many words they take, and
// what they do with the command after them.
func (s *wrapperSpec) options(words []Word) (int, outcome) {
	return scanOptions(words, s.optionSpec, s.does)
}

// does is what the wrapper's option opt (a letter, or a long name) does with its command.
func (s *wrapperSpec) does(opt string) outcome {
	switch {
	case s.replaced.has(opt):
		return replaced
	case s.runsNothing.has(opt):
		return runsNothing
	case s.moves.has(opt):
		return moved
	}
	return ran
}

// scanOptions walks the options at the start of words the way a wrapper or a carrier reads its
// own, up to its first operand (an unknown word is one) or after `--`: a cluster's letters up to
// one that takes a value, which takes the rest of the cluster or the next word, and a long option
// with its value after `=` or as the next word. does, when it is not nil, says what each option
// does: moved is remembered and the walk goes on, runsNothing and replaced end it. It returns the
// index of the first word after the options and the outcome.
func scanOptions(words []Word, values optionSpec, does func(opt string) outcome) (int, outcome) {
	how := ran
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case !w.Known || !isOptionText(w.Text):
			return i, how
		case w.Text == "--":
			return i + 1, how
		case strings.HasPrefix(w.Text, "--"):
			name, _, attached := strings.Cut(w.Text, "=")
			if does != nil {
				if o := does(name); o == moved {
					how = moved
				} else if o != ran {
					return i, o
				}
			}
			if slices.Contains(values.long, name) && !attached {
				i++
			}
		default:
			for j := 1; j < len(w.Text); j++ {
				if does != nil {
					if o := does(w.Text[j : j+1]); o == moved {
						how = moved
					} else if o != ran {
						return i, o
					}
				}
				if strings.IndexByte(values.short, w.Text[j]) >= 0 {
					if j+1 == len(w.Text) {
						i++
					}
					break
				}
			}
		}
	}
	return len(words), how
}

// afterOptions returns the words after a carrier's own options (scanOptions).
func afterOptions(words []Word, values optionSpec) []Word {
	n, _ := scanOptions(words, values, nil)
	return words[n:]
}

// isOptionText reports whether a word, by its text, is written as an option: a dash and
// something after it. A lone `-` is an operand (standard input).
func isOptionText(t string) bool {
	return len(t) > 1 && t[0] == '-'
}

// assignments is how many NAME=value words words begin with.
func assignments(words []Word) int {
	n := 0
	for n < len(words) && words[n].Known {
		name, _, ok := strings.Cut(words[n].Text, "=")
		if !ok || !isShellName(name) {
			break
		}
		n++
	}
	return n
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
