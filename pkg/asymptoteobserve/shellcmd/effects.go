package shellcmd

import (
	"path"
	"slices"
	"strings"
)

// Op is what a command evidently does to a file.
type Op string

const (
	// OpWrite creates or replaces a file's content: >, >|, &>, >&file, tee, the destination of cp
	// and install.
	OpWrite Op = "write"
	// OpAppend adds to a file: >>, &>>, tee -a.
	OpAppend Op = "append"
	// OpTouch creates a file or updates its times (touch).
	OpTouch Op = "touch"
	// OpTruncate sets a file's size (truncate).
	OpTruncate Op = "truncate"
	// OpEdit edits a file in place (sed -i, perl -i).
	OpEdit Op = "edit"
	// OpDelete removes a file (rm, unlink, git rm).
	OpDelete Op = "delete"
	// OpMoveFrom is a source of mv or git mv, which stops existing.
	OpMoveFrom Op = "move_from"
	// OpMoveTo is the destination of mv or git mv.
	OpMoveTo Op = "move_to"
	// OpLink is a link ln creates.
	OpLink Op = "link"
)

// FileEffect is a file a command evidently writes, and how.
type FileEffect struct {
	Path string
	Op   Op
}

// FileEffects returns the file effects of every command, in order.
func FileEffects(commands []Command) []FileEffect {
	var effects []FileEffect
	for _, c := range commands {
		effects = append(effects, c.FileEffects()...)
	}
	return effects
}

// FileEffects returns the files the command evidently writes: the targets of its output
// redirections, then the operands of rm, unlink, touch, truncate, mv, cp, install, ln, tee,
// `git mv`/`git rm` and in-place sed/perl. Anything it cannot read plainly -- an unknown word, a
// command that writes files itself -- contributes nothing, and neither does a wrapper that moves
// the command (sudo -i). Paths are as written: relative to the directory the command runs in,
// which after a cd (AfterCD) is not the one the command line started in.
func (c Command) FileEffects() []FileEffect {
	var effects []FileEffect
	for _, r := range c.Redirects {
		if path, op, ok := redirectTarget(r); ok {
			effects = append(effects, FileEffect{Path: path, Op: op})
		}
	}
	for _, e := range c.commandTargets() {
		if e.Path != "" {
			effects = append(effects, e)
		}
	}
	return effects
}

// redirectTarget returns the file an output redirection writes. Input redirections, here-documents
// and here-strings write nothing, and neither does a duplicated descriptor (2>&1) or a device.
func redirectTarget(r Redirect) (string, Op, bool) {
	op := OpWrite
	switch r.Op {
	case ">", ">|", "&>":
	case ">>", "&>>":
		op = OpAppend
	case ">&":
		// >&word writes a file unless word names a descriptor.
		if t := r.Target; !t.Known || t.Text == "-" || strings.Trim(t.Text, "0123456789") == "" {
			return "", "", false
		}
	default:
		return "", "", false
	}
	t := r.Target
	if !t.Known || t.Text == "" || isDevice(t.Text) {
		return "", "", false
	}
	return t.Text, op, true
}

// optionSpec names a command's options that take a value: a short one takes the rest of its
// cluster or the next word, a long one what follows `=` or the next word.
type optionSpec struct {
	short string
	long  []string
}

var valueOptions = map[string]optionSpec{
	"touch":    {short: "dtr", long: []string{"--date", "--reference"}},
	"truncate": {short: "rs", long: []string{"--reference", "--size"}},
	"cp":       {short: "St", long: []string{"--suffix", "--target-directory"}},
	"mv":       {short: "St", long: []string{"--suffix", "--target-directory"}},
	"ln":       {short: "St", long: []string{"--suffix", "--target-directory"}},
	"install":  {short: "gmoSt", long: []string{"--group", "--mode", "--owner", "--suffix", "--target-directory"}},
}

// flagOptions are the value-less options that change what a command does to its operands.
var flagOptions = map[string]optionSpec{
	"tee":     {short: "a", long: []string{"--append"}},
	"cp":      {short: "T", long: []string{"--no-target-directory", "--parents"}},
	"mv":      {short: "T", long: []string{"--no-target-directory"}},
	"ln":      {short: "T", long: []string{"--no-target-directory"}},
	"install": {short: "dT", long: []string{"--directory", "--no-target-directory"}},
	"git-mv":  {short: "n", long: []string{"--dry-run"}},
}

// longOptions are every long option of the GNU commands and git mv, which parseOptions reads, so
// that an abbreviation resolves to the one option it begins, as getopt_long and git do.
var longOptions = map[string][]string{
	"git-mv":   {"--dry-run", "--force", "--sparse", "--verbose"},
	"rm":       {"--dir", "--force", "--help", "--interactive", "--no-preserve-root", "--one-file-system", "--preserve-root", "--recursive", "--verbose", "--version"},
	"unlink":   {"--help", "--version"},
	"touch":    {"--date", "--help", "--no-create", "--no-dereference", "--reference", "--time", "--version"},
	"truncate": {"--help", "--io-blocks", "--no-create", "--reference", "--size", "--version"},
	"tee":      {"--append", "--help", "--ignore-interrupts", "--output-error", "--version"},
	"cp": {"--archive", "--attributes-only", "--backup", "--context", "--copy-contents", "--debug", "--dereference",
		"--force", "--help", "--interactive", "--keep-directory-symlink", "--link", "--no-clobber", "--no-dereference",
		"--no-preserve", "--no-target-directory", "--one-file-system", "--parents", "--preserve", "--recursive",
		"--reflink", "--remove-destination", "--sparse", "--strip-trailing-slashes", "--suffix", "--symbolic-link",
		"--target-directory", "--update", "--verbose", "--version"},
	"mv": {"--backup", "--context", "--debug", "--exchange", "--force", "--help", "--interactive", "--no-clobber",
		"--no-copy", "--no-target-directory", "--strip-trailing-slashes", "--suffix", "--target-directory", "--update",
		"--verbose", "--version"},
	"ln": {"--backup", "--directory", "--force", "--help", "--interactive", "--logical", "--no-dereference",
		"--no-target-directory", "--physical", "--relative", "--suffix", "--symbolic", "--target-directory",
		"--verbose", "--version"},
	"install": {"--backup", "--compare", "--context", "--debug", "--directory", "--group", "--help", "--mode",
		"--no-target-directory", "--owner", "--preserve-context", "--preserve-timestamps", "--strip",
		"--strip-program", "--suffix", "--target-directory", "--verbose", "--version"},
}

// parsedOptions are a command's operands, the values of its value options keyed by the short
// letter or the long name, and the flag options it was given.
type parsedOptions struct {
	operands []Word
	values   map[string]Word
	flags    map[string]bool
}

// optionTable is what a command's words are split by: its options that take a value, its
// value-less options the caller asks about, and every long option it has, so that an abbreviation
// resolves to the one option it begins.
type optionTable struct {
	values optionSpec
	flags  optionSpec
	long   []string
}

// parseOptions splits a command's words into operands, the values of its valueOptions and its
// flagOptions. Everything after `--` is an operand, and so is an unknown word. A long option may
// be abbreviated to a prefix of one of its longOptions; an abbreviation of several fails the
// command, which then has no operands.
func parseOptions(command string, words []Word) parsedOptions {
	return optionTable{valueOptions[command], flagOptions[command], longOptions[command]}.parse(words, false)
}

// parse splits words by the table. An unknown word is an operand, unless written is set: then a
// word is read as it is written (Word.Unquoted), so `-n"$N"` and `--file=$F` are still options.
func (t optionTable) parse(words []Word, written bool) parsedOptions {
	var p parsedOptions
	for i := 0; i < len(words); i++ {
		w := words[i]
		text, known := w.Text, w.Known
		if written {
			text, known = w.Unquoted, true
		}
		switch {
		case !known || text == "-" || !strings.HasPrefix(text, "-"):
			p.operands = append(p.operands, w)
		case text == "--":
			p.operands = append(p.operands, words[i+1:]...)
			return p
		case strings.HasPrefix(text, "--"):
			name, value, attached := strings.Cut(text, "=")
			name, ok := longOption(t.long, name)
			if !ok {
				return parsedOptions{}
			}
			if slices.Contains(t.flags.long, name) {
				p.flag(name)
			}
			if !slices.Contains(t.values.long, name) {
				continue
			}
			switch {
			case attached:
				p.value(name, attachedValue(w, value))
			case i+1 < len(words):
				i++
				p.value(name, words[i])
			default:
				p.value(name, Word{})
			}
		default:
			for j := 1; j < len(text); j++ {
				c := text[j]
				if !w.Known && (c == '$' || c == '`') {
					break // what follows in an unknown word is not option letters
				}
				if strings.IndexByte(t.flags.short, c) >= 0 {
					p.flag(string(c))
				}
				if strings.IndexByte(t.values.short, c) < 0 {
					continue
				}
				switch {
				case j+1 < len(text):
					p.value(string(c), attachedValue(w, text[j+1:]))
				case i+1 < len(words):
					i++
					p.value(string(c), words[i])
				default:
					p.value(string(c), Word{})
				}
				break
			}
		}
	}
	return p
}

// flag and value record an option. Most commands are given none the table asks about, so the maps
// are made when the first one is.
func (p *parsedOptions) flag(name string) {
	if p.flags == nil {
		p.flags = map[string]bool{}
	}
	p.flags[name] = true
}

func (p *parsedOptions) value(name string, value Word) {
	if p.values == nil {
		p.values = map[string]Word{}
	}
	p.values[name] = value
}

// attachedValue is the value an option carries in its own word w (--suffix=.bak, -S.bak).
func attachedValue(w Word, value string) Word {
	v := Word{Unquoted: value, Known: w.Known}
	if v.Known {
		v.Text = value
	}
	return v
}

// longOption resolves a long option name the way getopt_long and git do: the name itself, or the
// one long option of all it is a prefix of. A prefix of several is ambiguous (not ok); a name that
// is none of them stays as written.
func longOption(all []string, name string) (string, bool) {
	if slices.Contains(all, name) {
		return name, true
	}
	match := ""
	for _, option := range all {
		if strings.HasPrefix(option, name) {
			if match != "" {
				return "", false
			}
			match = option
		}
	}
	if match == "" {
		return name, true
	}
	return match, true
}

// namesFile reports whether p can name a file a command creates: a last element of ., .. or /
// names a directory that already exists.
func namesFile(p string) bool {
	base := path.Base(p)
	return base != "." && base != ".." && base != "/"
}

// writtenAsDirectory reports whether w is written as a directory: with a trailing slash, or ending
// in . or ..
func writtenAsDirectory(w Word) bool {
	return w.Known && w.Text != "" && (strings.HasSuffix(w.Text, "/") || !namesFile(w.Text))
}

func (c Command) commandTargets() []FileEffect {
	name := c.Name()
	if name == "" {
		return nil
	}
	words, _ := c.Unwrapped()
	args := words[1:]
	if name == "git" && (c.Is("git", "mv") || c.Is("git", "rm")) {
		name, args = "git-"+args[0].Text, args[1:]
	}
	switch name {
	case "rm", "unlink", "git-rm":
		return effectsOf(parseOptions(name, args).operands, OpDelete)
	case "touch":
		return effectsOf(parseOptions(name, args).operands, OpTouch)
	case "truncate":
		return effectsOf(parseOptions(name, args).operands, OpTruncate)
	case "tee":
		opts := parseOptions(name, args)
		if opts.flags["a"] || opts.flags["--append"] {
			return effectsOf(opts.operands, OpAppend)
		}
		return effectsOf(opts.operands, OpWrite)
	case "mv", "git-mv", "cp", "install", "ln":
		opts := parseOptions(name, args)
		if opts.flags["d"] || opts.flags["--directory"] || opts.flags["n"] || opts.flags["--dry-run"] {
			// install -d creates its operands as directories; git mv -n only says what it would do.
			return nil
		}
		// mv's sources stop existing, so they are written too.
		moves := name == "mv" || name == "git-mv"
		dest := OpWrite
		switch {
		case moves:
			dest = OpMoveTo
		case name == "ln":
			dest = OpLink
		}
		sources := opts.operands
		dir, ok := opts.values["t"]
		if !ok {
			dir, ok = opts.values["--target-directory"]
		}
		parents := opts.flags["--parents"]
		if opts.flags["T"] || opts.flags["--no-target-directory"] {
			// -T makes the second of exactly two operands the destination itself; GNU refuses
			// any other count, and -T with -t.
			if ok || len(sources) != 2 {
				return nil
			}
		} else if !ok {
			switch n := len(sources); {
			case n == 1 && name == "ln":
				// ln TARGET links ./<base name of TARGET>.
				dir, ok = Word{Text: ".", Known: true}, true
			case n >= 3 || n == 2 && (parents || writtenAsDirectory(sources[1])):
				// Several sources, or a destination written as a directory, go into the last operand.
				dir, ok, sources = sources[n-1], true, sources[:n-1]
			}
		}
		if ok {
			// Every source is written as DIR/<base name>, or DIR/<source> with cp --parents.
			var written []FileEffect
			if moves {
				written = effectsOf(sources, OpMoveFrom)
			}
			if !dir.Known || dir.Text == "" {
				return written
			}
			// The command line is shell syntax, so its paths join with "/" on every host; gitlink's
			// RelPath converts them for the local filesystem.
			for _, src := range sources {
				if !src.Known || !namesFile(src.Text) {
					continue
				}
				file := path.Base(src.Text)
				if parents {
					file = src.Text
				}
				written = append(written, FileEffect{Path: path.Join(dir.Text, file), Op: dest})
			}
			return written
		}
		if moves {
			// Every operand but the last is a source. When the last is unknown, so is the
			// destination, and every known operand is a source.
			effects := effectsOf(sources, OpMoveFrom)
			if n := len(sources); n >= 2 && sources[n-1].Known {
				effects[len(effects)-1].Op = OpMoveTo
			}
			return effects
		}
		if len(sources) == 2 && namesFile(sources[1].Text) {
			return effectsOf(sources[1:], dest)
		}
	case "sed", "perl", "gsed":
		return effectsOf(inPlaceFiles(name, args), OpEdit)
	}
	return nil
}

// inPlaceFiles returns the files sed or perl edits in place, and nothing without -i/--in-place.
func inPlaceFiles(name string, args []Word) []Word {
	files, inPlace := scriptFiles(name, args)
	if !inPlace {
		return nil
	}
	return files
}

// scriptFiles returns the files sed or perl works on, and whether it edits them in place
// (-i/--in-place). They are every operand when the script came in with -e/-f (attached, separate,
// or --expression/--file), and every operand after the first, the script, otherwise.
func scriptFiles(name string, args []Word) (files []Word, inPlace bool) {
	// A script switch brings the script, attached or as the next word. -i and a valued switch take
	// the rest of the cluster as their value, so an i or e after one (-i.bak, perl -Mwarnings,
	// -Ilib) is part of that value.
	script, valued := "ef", "l"
	if name == "perl" {
		script, valued = "eE", "CDFIMdmx0"
	}
	scripted := false
	var operands []Word
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !a.Known || a.Text == "-" || !strings.HasPrefix(a.Text, "-"):
			operands = append(operands, a)
		case a.Text == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case a.Text == "--in-place" || strings.HasPrefix(a.Text, "--in-place="):
			inPlace = true
		case a.Text == "--expression" || a.Text == "--file":
			scripted = true
			i++
		case strings.HasPrefix(a.Text, "--expression=") || strings.HasPrefix(a.Text, "--file="):
			scripted = true
		case a.Text == "--line-length":
			i++
		case strings.HasPrefix(a.Text, "--"):
		default:
			for j := 1; j < len(a.Text); j++ {
				c := a.Text[j]
				if c == 'i' {
					inPlace = true
					// BSD sed reads its backup suffix from the next word; macOS agents pass ''.
					if name == "sed" && j+1 == len(a.Text) && i+1 < len(args) && args[i+1].Known && args[i+1].Text == "" {
						i++
					}
					break
				}
				if strings.IndexByte(script, c) >= 0 {
					scripted = true
					if j+1 == len(a.Text) {
						i++ // the script is the next word
					}
					break
				}
				if strings.IndexByte(valued, c) >= 0 {
					if name != "perl" && j+1 == len(a.Text) {
						i++ // sed -l N
					}
					break
				}
			}
		}
	}
	if !scripted && len(operands) > 0 {
		operands = operands[1:]
	}
	return operands, inPlace
}

// effectsOf are the known words of words, each with op.
func effectsOf(words []Word, op Op) []FileEffect {
	var out []FileEffect
	for _, w := range words {
		if w.Known {
			out = append(out, FileEffect{Path: w.Text, Op: op})
		}
	}
	return out
}
