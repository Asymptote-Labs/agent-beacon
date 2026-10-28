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
	if !t.Known || t.Text == "" || strings.HasPrefix(t.Text, "/dev/") {
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
	"tee": {short: "a", long: []string{"--append"}},
}

// parsedOptions are a command's operands, the values of its value options keyed by the short
// letter or the long name, and the flag options it was given.
type parsedOptions struct {
	operands []Word
	values   map[string]Word
	flags    map[string]bool
}

// parseOptions splits words into operands, the values of spec's options and the flags of flags.
// Everything after `--` is an operand, and so is an unknown word.
func parseOptions(words []Word, spec, flags optionSpec) parsedOptions {
	p := parsedOptions{values: map[string]Word{}, flags: map[string]bool{}}
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case !w.Known || w.Text == "-" || !strings.HasPrefix(w.Text, "-"):
			p.operands = append(p.operands, w)
		case w.Text == "--":
			p.operands = append(p.operands, words[i+1:]...)
			return p
		case strings.HasPrefix(w.Text, "--"):
			name, value, attached := strings.Cut(w.Text, "=")
			if slices.Contains(flags.long, name) {
				p.flags[name] = true
			}
			if !slices.Contains(spec.long, name) {
				continue
			}
			switch {
			case attached:
				p.values[name] = Word{Text: value, Known: true}
			case i+1 < len(words):
				i++
				p.values[name] = words[i]
			default:
				p.values[name] = Word{}
			}
		default:
			for j := 1; j < len(w.Text); j++ {
				c := w.Text[j]
				if strings.IndexByte(flags.short, c) >= 0 {
					p.flags[string(c)] = true
				}
				if strings.IndexByte(spec.short, c) < 0 {
					continue
				}
				switch {
				case j+1 < len(w.Text):
					p.values[string(c)] = Word{Text: w.Text[j+1:], Known: true}
				case i+1 < len(words):
					i++
					p.values[string(c)] = words[i]
				default:
					p.values[string(c)] = Word{}
				}
				break
			}
		}
	}
	return p
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
		return effectsOf(parseOptions(args, valueOptions[name], optionSpec{}).operands, OpDelete)
	case "touch":
		return effectsOf(parseOptions(args, valueOptions[name], optionSpec{}).operands, OpTouch)
	case "truncate":
		return effectsOf(parseOptions(args, valueOptions[name], optionSpec{}).operands, OpTruncate)
	case "tee":
		opts := parseOptions(args, valueOptions[name], flagOptions[name])
		if opts.flags["a"] || opts.flags["--append"] {
			return effectsOf(opts.operands, OpAppend)
		}
		return effectsOf(opts.operands, OpWrite)
	case "mv", "git-mv", "cp", "install", "ln":
		opts := parseOptions(args, valueOptions[name], optionSpec{})
		// mv's sources stop existing, so they are written too.
		moves := name == "mv" || name == "git-mv"
		dest := OpWrite
		switch {
		case moves:
			dest = OpMoveTo
		case name == "ln":
			dest = OpLink
		}
		dir, ok := opts.values["t"]
		if !ok {
			dir, ok = opts.values["--target-directory"]
		}
		if ok {
			// -t DIR makes every operand a source, written as DIR/<base name>.
			var written []FileEffect
			if moves {
				written = effectsOf(opts.operands, OpMoveFrom)
			}
			if !dir.Known {
				return written
			}
			// The command line is shell syntax, so its paths join with "/" on every host; gitlink's
			// RelPath converts them for the local filesystem.
			for _, src := range opts.operands {
				if src.Known {
					written = append(written, FileEffect{Path: path.Join(dir.Text, path.Base(src.Text)), Op: dest})
				}
			}
			return written
		}
		if moves {
			// Every operand but the last is a source. When the last is unknown, so is the
			// destination, and every known operand is a source.
			effects := effectsOf(opts.operands, OpMoveFrom)
			if n := len(opts.operands); n >= 2 && opts.operands[n-1].Known {
				effects[len(effects)-1].Op = OpMoveTo
			}
			return effects
		}
		if len(opts.operands) >= 2 {
			return effectsOf(opts.operands[len(opts.operands)-1:], dest)
		}
	case "sed", "perl", "gsed":
		return effectsOf(inPlaceFiles(name, args), OpEdit)
	}
	return nil
}

// inPlaceFiles returns the files sed or perl edits in place, and nothing without -i/--in-place.
// They are every operand when the script came in with -e/-f (attached, separate, or
// --expression/--file), and every operand after the first, the script, otherwise.
func inPlaceFiles(name string, args []Word) []Word {
	// A script switch brings the script, attached or as the next word. -i and a valued switch take
	// the rest of the cluster as their value, so an i or e after one (-i.bak, perl -Mwarnings,
	// -Ilib) is part of that value.
	script, valued := "ef", "l"
	if name == "perl" {
		script, valued = "eE", "CDFIMdmx0"
	}
	inPlace, scripted := false, false
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
	if !inPlace {
		return nil
	}
	if !scripted {
		if len(operands) < 2 {
			return nil
		}
		operands = operands[1:]
	}
	return operands
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
