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

// writesContent reports whether the write puts content in a file a later command of the line
// could read or run: not a deletion, an emptying or a touch. A device is no write (isFile).
func (e FileEffect) writesContent() bool {
	switch e.Op {
	case OpWrite, OpAppend, OpEdit, OpMoveTo, OpLink:
		return true
	}
	return false
}

// write is a file a command writes, as FileWrites reports it.
type write struct {
	FileEffect
	// exact is set when the write is also an effect (FileEffects): every word that decided its
	// path, and how the command's operands split, is known, and it names a file rather than a
	// directory files are put into.
	exact bool
	// redirect is set for the target of a redirection, and stdout for one of the command's
	// standard output, which receives what the command writes there.
	redirect, stdout bool
}

// FileEffects returns the files the command evidently writes: the targets of its output
// redirections, then the operands of rm, unlink, shred, touch, truncate, mv, cp, install, ln,
// tee, `git mv`/`git rm` and in-place sed/perl, and the files a download or a copy from another
// host saves. Anything it cannot read plainly -- an unknown word, a command that writes files
// itself -- contributes nothing, and neither does a wrapper that moves the command (sudo -i).
// Paths are as written: relative to the directory the command runs in, which after a cd
// (AfterCD) is not the one the command line started in.
func (c Command) FileEffects() []FileEffect {
	return fileEffects(c.writes(), true)
}

// FileWrites returns the files the command evidently writes, as FileEffects does, and also the
// ones whose path depends on something this package does not evaluate: ~/.bashrc, $HOME/.netrc
// and "$dir/out.log" are reported as those words, without their quotes (Word.Unquoted). The two
// answer different questions, as FileEffects and FileReads do. An effect is the exact file a
// command wrote, for a caller that goes on to find that file. A write is asked what it names:
// ~/.ssh/authorized_keys names a login's authorized keys whatever the home directory is. Both
// come from one reading of the command (writes), so every effect is a write, in the same order.
//
// A write may also be a directory, written with a trailing slash: the one an archive is
// extracted into (tar -x -C, unzip -d), a repository is cloned into (git clone, gh repo clone)
// or rsync copies a directory's contents into. Which files land there is not on the command
// line, so it is no effect; the directory the command runs in is "./".
func (c Command) FileWrites() []FileEffect {
	return fileEffects(c.writes(), false)
}

// fileEffects are the files of writes, or of the exact ones.
func fileEffects(writes []write, exact bool) []FileEffect {
	var out []FileEffect
	for _, w := range writes {
		if w.exact || !exact {
			out = append(out, w.FileEffect)
		}
	}
	return out
}

// writes returns the files the command writes: the targets of its output redirections, then the
// files its own words name (commandWrites).
func (c *Command) writes() []write {
	var out []write
	for k := range c.Redirects {
		r := &c.Redirects[k]
		if path, op, exact, ok := redirectTarget(r); ok {
			out = append(out, write{FileEffect: FileEffect{Path: path, Op: op}, exact: exact, redirect: true, stdout: r.ofStdout()})
		}
	}
	return append(out, c.commandWrites()...)
}

// wordPath returns the path a word names, as it is written without its quotes (Word.Unquoted),
// and whether that is its value (exact).
func wordPath(w Word) (path string, exact bool) {
	return w.Unquoted, w.Known
}

// literalWord is a word this package makes up, written as its value: the "." a command
// writes into when it names no directory.
func literalWord(s string) Word {
	return Word{Text: s, Raw: s, Known: true, Unquoted: s}
}

// attachedValue is the value an option carries in its own word w (--suffix=.bak, -S.bak), or
// another part of w (the file name a URL ends in): known when w is.
func attachedValue(w Word, value string) Word {
	v := Word{Unquoted: value, Known: w.Known}
	if v.Known {
		v.Text = value
	}
	return v
}

// redirectTarget returns the file an output redirection writes. Input redirections, here-documents
// and here-strings write nothing, and neither does a duplicated descriptor (2>&1) or a device.
func redirectTarget(r *Redirect) (path string, op Op, exact, ok bool) {
	op = OpWrite
	switch r.kind() {
	case redirOut, redirOutErr:
	case redirAppend, redirOutErrAppend:
		op = OpAppend
	case redirDupOut:
		// >&word writes a file unless word names a descriptor, which an unknown word may.
		if t := r.Target; !t.Known || isDescriptor(t.Text) {
			return "", "", false, false
		}
	default:
		return "", "", false, false
	}
	if path, exact = wordPath(r.Target); !isFile(path) {
		return "", "", false, false
	}
	return path, op, exact, true
}

// optionSpec names a command's options that take a value: a short one takes the rest of its
// cluster or the next word, a long one what follows `=` or the next word.
type optionSpec struct {
	short string
	long  []string
}

// has reports whether opt, a letter or a long name with its dashes, is one of the options.
func (s optionSpec) has(opt string) bool {
	if len(opt) == 1 {
		return strings.IndexByte(s.short, opt[0]) >= 0
	}
	return slices.Contains(s.long, opt)
}

// writers are how parseOptions reads the commands whose operands are the files they write: the
// options that take a value, the value-less ones that change what the command does to its
// operands, and every long option of the GNU commands and git mv, so that an abbreviation
// resolves to the one option it begins, as getopt_long and git do.
var writers = map[string]optionTable{
	"rm":     {long: []string{"--dir", "--force", "--help", "--interactive", "--no-preserve-root", "--one-file-system", "--preserve-root", "--recursive", "--verbose", "--version"}},
	"unlink": {long: []string{"--help", "--version"}},
	"shred": {
		values: optionSpec{short: "ns", long: []string{"--iterations", "--random-source", "--size"}},
		flags:  optionSpec{short: "u", long: []string{"--remove"}},
		long:   []string{"--exact", "--force", "--help", "--iterations", "--random-source", "--remove", "--size", "--verbose", "--version", "--zero"},
	},
	"touch": {
		values: optionSpec{short: "dtr", long: []string{"--date", "--reference"}},
		long:   []string{"--date", "--help", "--no-create", "--no-dereference", "--reference", "--time", "--version"},
	},
	"truncate": {
		values: optionSpec{short: "rs", long: []string{"--reference", "--size"}},
		long:   []string{"--help", "--io-blocks", "--no-create", "--reference", "--size", "--version"},
	},
	"tee": {
		flags: optionSpec{short: "a", long: []string{"--append"}},
		long:  []string{"--append", "--help", "--ignore-interrupts", "--output-error", "--version"},
	},
	"cp": {
		values: optionSpec{short: "St", long: []string{"--suffix", "--target-directory"}},
		flags:  optionSpec{short: "T", long: []string{"--no-target-directory", "--parents"}},
		long: []string{"--archive", "--attributes-only", "--backup", "--context", "--copy-contents", "--debug", "--dereference",
			"--force", "--help", "--interactive", "--keep-directory-symlink", "--link", "--no-clobber", "--no-dereference",
			"--no-preserve", "--no-target-directory", "--one-file-system", "--parents", "--preserve", "--recursive",
			"--reflink", "--remove-destination", "--sparse", "--strip-trailing-slashes", "--suffix", "--symbolic-link",
			"--target-directory", "--update", "--verbose", "--version"},
	},
	"mv": {
		values: optionSpec{short: "St", long: []string{"--suffix", "--target-directory"}},
		flags:  optionSpec{short: "T", long: []string{"--no-target-directory"}},
		long: []string{"--backup", "--context", "--debug", "--exchange", "--force", "--help", "--interactive", "--no-clobber",
			"--no-copy", "--no-target-directory", "--strip-trailing-slashes", "--suffix", "--target-directory", "--update",
			"--verbose", "--version"},
	},
	"ln": {
		values: optionSpec{short: "St", long: []string{"--suffix", "--target-directory"}},
		flags:  optionSpec{short: "T", long: []string{"--no-target-directory"}},
		long: []string{"--backup", "--directory", "--force", "--help", "--interactive", "--logical", "--no-dereference",
			"--no-target-directory", "--physical", "--relative", "--suffix", "--symbolic", "--target-directory",
			"--verbose", "--version"},
	},
	"install": {
		values: optionSpec{short: "gmoSt", long: []string{"--group", "--mode", "--owner", "--suffix", "--target-directory"}},
		flags:  optionSpec{short: "dT", long: []string{"--directory", "--no-target-directory"}},
		long: []string{"--backup", "--compare", "--context", "--debug", "--directory", "--group", "--help", "--mode",
			"--no-target-directory", "--owner", "--preserve-context", "--preserve-timestamps", "--strip",
			"--strip-program", "--suffix", "--target-directory", "--verbose", "--version"},
	},
	"git-mv": {
		flags: optionSpec{short: "n", long: []string{"--dry-run"}},
		long:  []string{"--dry-run", "--force", "--sparse", "--verbose"},
	},
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

// parseOptions splits the words of a command of writers into operands, values and flags.
func parseOptions(command string, words []Word) parsedOptions {
	return writers[command].parse(words)
}

// parse splits words by the table. Everything after `--` is an operand. A word is read as it is
// written (Word.Unquoted), so `-n"$N"` and `--file=$F` are still options, and one whose value is
// unknown otherwise is an operand. A long option may be abbreviated to a prefix of one of the
// table's long options; an abbreviation of several fails the command, which then has no
// operands.
func (t optionTable) parse(words []Word) parsedOptions {
	var p parsedOptions
	for i := 0; i < len(words); i++ {
		w := words[i]
		text := w.Unquoted
		switch {
		case !isOptionText(text):
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

// valueLetter returns the index, in the option cluster t (a word's text as written), of the
// first letter that is one of values, which takes the rest of the cluster or the next word as
// its value; -1 when no such letter comes before the end, or, in a word whose value is not
// known, before a $ or a backtick, after which nothing is option letters.
func valueLetter(t string, known bool, values string) int {
	for j := 1; j < len(t); j++ {
		switch {
		case !known && (t[j] == '$' || t[j] == '`'):
			return -1
		case strings.IndexByte(values, t[j]) >= 0:
			return j
		}
	}
	return -1
}

// clusterEnd is where the option letters before the value of the cluster t end: at its value
// letter j (valueLetter) when there is one, and otherwise at the end of the word or, in a word
// whose value is not known, at its first $ or backtick.
func clusterEnd(t string, known bool, j int) int {
	if j >= 0 {
		return j
	}
	if k := strings.IndexAny(t, "$`"); !known && k >= 0 {
		return k
	}
	return len(t)
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
	p, _ := wordPath(w)
	return p != "" && (strings.HasSuffix(p, "/") || !namesFile(p))
}

// commandWrites returns the files the command's own words say it writes.
func (c *Command) commandWrites() []write {
	name, args := c.program()
	if name == "git" && (isSub(args, "mv") || isSub(args, "rm")) {
		name, args = "git-"+args[0].Text, args[1:]
	}
	switch name {
	case "":
		return nil
	case "rm", "unlink", "git-rm":
		return writesOf(parseOptions(name, args).operands, OpDelete)
	case "touch":
		return writesOf(parseOptions(name, args).operands, OpTouch)
	case "truncate":
		return writesOf(parseOptions(name, args).operands, OpTruncate)
	case "shred":
		// shred overwrites its operands, and with -u removes them.
		opts := parseOptions(name, args)
		if opts.flags["u"] || opts.flags["--remove"] {
			return writesOf(opts.operands, OpDelete)
		}
		return writesOf(opts.operands, OpWrite)
	case "tee":
		opts := parseOptions(name, args)
		if opts.flags["a"] || opts.flags["--append"] {
			return writesOf(opts.operands, OpAppend)
		}
		return writesOf(opts.operands, OpWrite)
	case "mv", "git-mv", "cp", "install", "ln":
		return copyWrites(name, args)
	case "sed", "perl":
		return writesOf(inPlaceFiles(name, args), OpEdit)
	case "curl":
		return curlOutputs(args)
	case "wget":
		return wgetOutputs(args)
	case "dd":
		return ddOutput(args)
	case "scp", "rsync":
		return copiedHere(name, args)
	}
	// A directory an archive is extracted or a repository is cloned into is a write, not an
	// effect: which files land there is not on the command line.
	if dir, ok := intoDirectory(name, args); ok {
		return []write{{FileEffect: FileEffect{Path: dir, Op: OpWrite}}}
	}
	return nil
}

// copyPlan is how cp, install, ln, mv and git mv read their operands: the sources, and where
// they go, dest itself or, when into is set, the directory dest, where each source lands by its
// base name, or by its whole path with cp --parents.
type copyPlan struct {
	sources       []Word
	dest          Word
	into, parents bool
}

// copyOperands reads the operands of cp, install, ln, mv or git mv. ok is false when the command
// writes none of the files they name: install -d creates them as directories and git mv -n only
// says what it would do; -T with -t or with other than two operands is refused; and one operand
// names no destination, except ln's, which links ./<its base name>.
func copyOperands(name string, args []Word) (copyPlan, bool) {
	opts := parseOptions(name, args)
	if opts.flags["d"] || opts.flags["--directory"] || opts.flags["n"] || opts.flags["--dry-run"] {
		return copyPlan{}, false
	}
	p := copyPlan{sources: opts.operands, parents: opts.flags["--parents"]}
	dir, ok := opts.values["t"]
	if !ok {
		dir, ok = opts.values["--target-directory"]
	}
	n := len(p.sources)
	switch {
	case opts.flags["T"] || opts.flags["--no-target-directory"]:
		// -T makes the second of exactly two operands the destination itself.
		if ok || n != 2 {
			return copyPlan{}, false
		}
		p.dest, p.sources = p.sources[1], p.sources[:1]
	case ok:
		p.dest, p.into = dir, true
	case n == 1 && name == "ln":
		p.dest, p.into = literalWord("."), true
	case n >= 3 || n == 2 && (p.parents || writtenAsDirectory(p.sources[1])):
		// Several sources, or a destination written as a directory, go into the last operand.
		p.dest, p.into, p.sources = p.sources[n-1], true, p.sources[:n-1]
	case n == 2:
		p.dest, p.sources = p.sources[1], p.sources[:1]
	default:
		return copyPlan{}, false
	}
	return p, true
}

// copyWrites returns what cp, install, ln, mv or git mv writes: mv's sources stop existing, and
// the destination is written (mv's moved to, ln's linked).
func copyWrites(name string, args []Word) []write {
	p, ok := copyOperands(name, args)
	if !ok {
		return nil
	}
	op := OpWrite
	var out []write
	switch name {
	case "mv", "git-mv":
		op, out = OpMoveTo, writesOf(p.sources, OpMoveFrom)
	case "ln":
		op = OpLink
	}
	to, toExact := wordPath(p.dest)
	if !p.into {
		if isFile(to) {
			out = append(out, write{FileEffect: FileEffect{Path: to, Op: op}, exact: toExact})
		}
		return out
	}
	if to == "" {
		return out
	}
	// The command line is shell syntax, so its paths join with "/" on every host; gitlink's
	// RelPath converts them for the local filesystem.
	for _, src := range p.sources {
		from, exact := wordPath(src)
		if from == "" || !namesFile(from) {
			continue
		}
		file := path.Base(from)
		if p.parents {
			file = from
		}
		if joined := path.Join(to, file); isFile(joined) {
			out = append(out, write{FileEffect: FileEffect{Path: joined, Op: op}, exact: exact && toExact})
		}
	}
	return out
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
		case !a.Known || !isOptionText(a.Text):
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

// writesOf are the words of words that name a file (isFile), each written with op.
func writesOf(words []Word, op Op) []write {
	var out []write
	for _, w := range words {
		if p, exact := wordPath(w); isFile(p) {
			out = append(out, write{FileEffect: FileEffect{Path: p, Op: op}, exact: exact})
		}
	}
	return out
}
