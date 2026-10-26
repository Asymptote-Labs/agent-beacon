package gitlink

import (
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// ShellWrittenPaths returns the files a shell command line evidently writes: the operands of rm,
// touch, truncate, mv, cp, install, ln, tee, `git mv`/`git rm`, in-place sed/perl, and the targets
// of output redirections. It is deliberately shallow. Anything it cannot read plainly -- variables,
// command substitutions, globs, a script that writes files itself -- contributes nothing, because
// a wrong path would link a session to a commit it had no part in, and a missed one only leaves a
// link out. A command line that does not parse was captured incomplete and contributes nothing.
func ShellWrittenPaths(command string) []string {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	var paths []string
	moved := false
	syntax.Walk(file, func(node syntax.Node) bool {
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		var found []string
		for _, r := range stmt.Redirs {
			if target, ok := redirectTarget(r); ok {
				found = append(found, target)
			}
		}
		call, _ := stmt.Cmd.(*syntax.CallExpr)
		var args []arg
		if call != nil {
			args = callArgs(call)
			found = append(found, commandTargets(args)...)
		}
		if moved {
			// After a cd, a relative path is relative to somewhere this parser does not track.
			found = absoluteOnly(found)
		}
		paths = append(paths, found...)
		if command := unwrapped(args); len(command) > 0 && command[0].known {
			switch command[0].text {
			case "cd", "pushd", "popd":
				moved = true
			}
		}
		return true
	})
	return dedupe(paths)
}

// arg is one word of a simple command after brace expansion. known is false when its value
// depends on something this parser does not evaluate: a parameter, a command or arithmetic
// substitution, a glob, or a leading tilde. An unknown word is never reported as a path.
type arg struct {
	text  string
	known bool
}

func callArgs(call *syntax.CallExpr) []arg {
	var args []arg
	for _, word := range call.Args {
		// SplitBraces rewrites the word's parts, so it works on a copy: the tree is still being walked.
		split := &syntax.Word{Parts: slices.Clone(word.Parts)}
		if !syntax.SplitBraces(split) {
			args = append(args, wordArg(word))
			continue
		}
		for _, expanded := range expand.Braces(split) {
			args = append(args, wordArg(expanded))
		}
	}
	return args
}

func wordArg(word *syntax.Word) arg {
	var b strings.Builder
	for i, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if i == 0 && strings.HasPrefix(p.Value, "~") {
				return arg{}
			}
			if !unescape(&b, p.Value, false) {
				return arg{}
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return arg{}
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return arg{}
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return arg{}
				}
				unescape(&b, lit.Value, true)
			}
		default:
			return arg{}
		}
	}
	return arg{text: b.String(), known: true}
}

// unescape writes a literal's value as the shell passes it on, removing the backslashes it
// removes. Outside quotes it reports false for an unescaped glob character, whose value depends on
// the file system.
func unescape(b *strings.Builder, s string, quoted bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (!quoted || strings.IndexByte("$`\"\\\n", s[i+1]) >= 0) {
			i++
			if s[i] != '\n' {
				b.WriteByte(s[i])
			}
			continue
		}
		if !quoted && (c == '*' || c == '?' || c == '[') {
			return false
		}
		b.WriteByte(c)
	}
	return true
}

// redirectTarget returns the file an output redirection writes. Input redirections, here-documents
// and here-strings write nothing, and neither does a duplicated descriptor (2>&1) or a device.
func redirectTarget(r *syntax.Redirect) (string, bool) {
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
	case syntax.DplOut:
		// >&word writes a file unless word names a descriptor.
		if target := wordArg(r.Word); !target.known || target.text == "-" || strings.Trim(target.text, "0123456789") == "" {
			return "", false
		}
	default:
		return "", false
	}
	target := wordArg(r.Word)
	if !target.known || target.text == "" || strings.HasPrefix(target.text, "/dev/") {
		return "", false
	}
	return target.text, true
}

// unwrapped skips the wrappers that run the real command in the same directory. A wrapper option
// that changes the directory or the command itself (sudo -i, env -C, env -S) leaves no command.
func unwrapped(args []arg) []arg {
	for len(args) > 0 && args[0].known {
		switch args[0].text {
		case "command", "builtin", "exec", "nohup", "time":
			args = args[1:]
		case "sudo":
			args = afterAssignments(afterOptions(args[1:], wrapperSudo))
		case "env":
			args = afterAssignments(afterOptions(args[1:], wrapperEnv))
		default:
			return args
		}
	}
	return args
}

type wrapperSpec struct {
	optionSpec
	blocking     string   // short options that move or replace the command
	blockingLong []string // long forms of the same
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
)

// afterOptions returns the words after a wrapper's own options, or nothing when one of them
// moves or replaces the command.
func afterOptions(args []arg, spec wrapperSpec) []arg {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !a.known || a.text == "-" || !strings.HasPrefix(a.text, "-"):
			return args[i:]
		case a.text == "--":
			return args[i+1:]
		case strings.HasPrefix(a.text, "--"):
			name, _, attached := strings.Cut(a.text, "=")
			if slices.Contains(spec.blockingLong, name) {
				return nil
			}
			if slices.Contains(spec.long, name) && !attached {
				i++
			}
		default:
			for j := 1; j < len(a.text); j++ {
				if strings.IndexByte(spec.blocking, a.text[j]) >= 0 {
					return nil
				}
				if strings.IndexByte(spec.short, a.text[j]) >= 0 {
					if j+1 == len(a.text) {
						i++
					}
					break
				}
			}
		}
	}
	return nil
}

func afterAssignments(args []arg) []arg {
	for len(args) > 0 && args[0].known {
		name, _, ok := strings.Cut(args[0].text, "=")
		if !ok || !isShellName(name) {
			break
		}
		args = args[1:]
	}
	return args
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

// parseOptions splits args into operands and the values of spec's options, keyed by the short
// letter or the long name. Everything after `--` is an operand, and so is an unknown word.
func parseOptions(args []arg, spec optionSpec) (operands []arg, values map[string]arg) {
	values = map[string]arg{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !a.known || a.text == "-" || !strings.HasPrefix(a.text, "-"):
			operands = append(operands, a)
		case a.text == "--":
			return append(operands, args[i+1:]...), values
		case strings.HasPrefix(a.text, "--"):
			name, value, attached := strings.Cut(a.text, "=")
			if !slices.Contains(spec.long, name) {
				continue
			}
			switch {
			case attached:
				values[name] = arg{text: value, known: true}
			case i+1 < len(args):
				i++
				values[name] = args[i]
			default:
				values[name] = arg{}
			}
		default:
			for j := 1; j < len(a.text); j++ {
				c := a.text[j]
				if strings.IndexByte(spec.short, c) < 0 {
					continue
				}
				switch {
				case j+1 < len(a.text):
					values[string(c)] = arg{text: a.text[j+1:], known: true}
				case i+1 < len(args):
					i++
					values[string(c)] = args[i]
				default:
					values[string(c)] = arg{}
				}
				break
			}
		}
	}
	return operands, values
}

func commandTargets(args []arg) []string {
	args = unwrapped(args)
	if len(args) == 0 || !args[0].known {
		return nil
	}
	name := filepath.Base(args[0].text)
	args = args[1:]
	if name == "git" && len(args) > 0 && args[0].known && (args[0].text == "mv" || args[0].text == "rm") {
		name, args = "git-"+args[0].text, args[1:]
	}
	switch name {
	case "rm", "unlink", "touch", "truncate", "git-rm", "tee":
		operands, _ := parseOptions(args, valueOptions[name])
		return knownTexts(operands)
	case "mv", "git-mv", "cp", "install", "ln":
		operands, values := parseOptions(args, valueOptions[name])
		// mv's sources stop existing, so they are written too.
		moves := name == "mv" || name == "git-mv"
		dir, ok := values["t"]
		if !ok {
			dir, ok = values["--target-directory"]
		}
		if ok {
			// -t DIR makes every operand a source, written as DIR/<base name>.
			var written []string
			if moves {
				written = knownTexts(operands)
			}
			if !dir.known {
				return written
			}
			for _, src := range operands {
				if src.known {
					written = append(written, filepath.Join(dir.text, filepath.Base(src.text)))
				}
			}
			return written
		}
		if moves {
			return knownTexts(operands)
		}
		if len(operands) >= 2 {
			return knownTexts(operands[len(operands)-1:])
		}
	case "sed", "perl", "gsed":
		return inPlaceFiles(name, args)
	}
	return nil
}

// inPlaceFiles returns the files sed or perl edits in place, and nothing without -i/--in-place.
// They are every operand when the script came in with -e/-f (attached, separate, or
// --expression/--file), and every operand after the first, the script, otherwise.
func inPlaceFiles(name string, args []arg) []string {
	// A script switch brings the script, attached or as the next word. -i and a valued switch take
	// the rest of the cluster as their value, so an i or e after one (-i.bak, perl -Mwarnings,
	// -Ilib) is part of that value.
	script, valued := "ef", "l"
	if name == "perl" {
		script, valued = "eE", "CDFIMdmx0"
	}
	inPlace, scripted := false, false
	var operands []arg
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !a.known || a.text == "-" || !strings.HasPrefix(a.text, "-"):
			operands = append(operands, a)
		case a.text == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case a.text == "--in-place" || strings.HasPrefix(a.text, "--in-place="):
			inPlace = true
		case a.text == "--expression" || a.text == "--file":
			scripted = true
			i++
		case strings.HasPrefix(a.text, "--expression=") || strings.HasPrefix(a.text, "--file="):
			scripted = true
		case a.text == "--line-length":
			i++
		case strings.HasPrefix(a.text, "--"):
		default:
			for j := 1; j < len(a.text); j++ {
				c := a.text[j]
				if c == 'i' {
					inPlace = true
					// BSD sed reads its backup suffix from the next word; macOS agents pass ''.
					if name == "sed" && j+1 == len(a.text) && i+1 < len(args) && args[i+1].known && args[i+1].text == "" {
						i++
					}
					break
				}
				if strings.IndexByte(script, c) >= 0 {
					scripted = true
					if j+1 == len(a.text) {
						i++ // the script is the next word
					}
					break
				}
				if strings.IndexByte(valued, c) >= 0 {
					if name != "perl" && j+1 == len(a.text) {
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
	return knownTexts(operands)
}

func knownTexts(args []arg) []string {
	var out []string
	for _, a := range args {
		if a.known {
			out = append(out, a.text)
		}
	}
	return out
}

// PatchPaths returns the files an apply_patch envelope touches: Add, Update, Delete, and the
// destination of a Move.
func PatchPaths(patch string) []string {
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				if p := strings.TrimSpace(rest); p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	return dedupe(paths)
}

func absoluteOnly(paths []string) []string {
	var out []string
	for _, p := range paths {
		if filepath.IsAbs(p) {
			out = append(out, p)
		}
	}
	return out
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
