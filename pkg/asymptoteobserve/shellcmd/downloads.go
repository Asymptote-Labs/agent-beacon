package shellcmd

import (
	"path"
	"slices"
	"strings"
)

// This file reads where a command puts what it fetches or unpacks: a file a download saves, the
// destination of a copy from another host, the output file of dd, and the directory an archive
// is extracted or a repository is cloned into.
//
// A file is an effect like any other write. A directory is not: which files land in it is not
// on the command line, so it is reported only among a command's writes (FileWrites), as the
// directory with a trailing slash. A caller that asks what a write names can then ask whether it
// is, or is under, a directory it cares about, and a caller that needs the exact file written
// (FileEffects) is told nothing it cannot use.

// wgetValueOptions are wget's short options that take a value.
const wgetValueOptions = "aoOiBtTwQPUelARDIX"

// gitCloneOptions are git clone's options that take a value.
var gitCloneOptions = optionTable{values: optionSpec{short: "boucj", long: []string{
	"--branch", "--origin", "--upload-pack", "--config", "--jobs", "--reference", "--reference-if-able",
	"--separate-git-dir", "--depth", "--shallow-since", "--shallow-exclude", "--filter", "--template",
	"--server-option", "--bundle-uri", "--revision",
}}}

// tarExtractOptions are tar's options that take a value, and the ones that say it extracts.
var tarExtractOptions = optionTable{values: tarOptions.values, flags: optionSpec{short: "x", long: []string{"--extract", "--get"}}}

// writesInto reports whether the command may put files into a directory it names (or the one
// it runs in), which only FileWrites reports.
func (c Command) writesInto() bool {
	switch program, args := c.program(); program {
	case "tar", "unzip", "rsync":
		return true
	case "git":
		return isSub(args, "clone")
	case "gh":
		return isSub(args, "repo", "clone")
	}
	return false
}

// intoDirectory returns the directory the command extracts or clones into, written with a
// trailing slash, when it does that. name and args are the command's program and the words
// after its name.
func intoDirectory(name string, args []Word) (string, bool) {
	dir, ok := Word{}, false
	switch {
	case name == "tar":
		dir, ok = tarExtractDirectory(args)
	case name == "unzip":
		dir, ok = unzipDirectory(args)
	case name == "git" && isSub(args, "clone"):
		dir, ok = cloneDirectory(gitCloneOptions.parse(args[1:], true).operands)
	case name == "gh" && isSub(args, "repo", "clone"):
		operands := args[2:]
		if i := slices.IndexFunc(operands, func(w Word) bool { return w.Unquoted == "--" }); i >= 0 {
			operands = operands[:i]
		}
		dir, ok = cloneDirectory(slices.DeleteFunc(slices.Clone(operands), isOption))
	}
	if !ok {
		return "", false
	}
	return asDirectory(dir.Unquoted), true
}

// asDirectory writes a path as a directory: with one trailing slash, and the directory a command
// runs in as "./".
func asDirectory(p string) string {
	if p = strings.TrimRight(p, "/"); p == "" || p == "." {
		return "./"
	}
	return p + "/"
}

// tarExtractDirectory returns where tar extracts to (-C, --directory, or the directory it runs
// in), and ok only when it extracts.
func tarExtractDirectory(args []Word) (Word, bool) {
	extracts := false
	dir := Word{Unquoted: "."}
	// The first word may be option letters without a dash (tar xzf in.tgz): each letter that
	// takes a value takes the next word, in order.
	if len(args) > 0 {
		if bundle := args[0].Unquoted; bundle != "" && isLetters(bundle) {
			values := 1
			for i := 0; i < len(bundle); i++ {
				extracts = extracts || bundle[i] == 'x'
				if strings.IndexByte(tarOptions.values.short, bundle[i]) >= 0 {
					if bundle[i] == 'C' && values < len(args) {
						dir = args[values]
					}
					values++
				}
			}
			if values > len(args) {
				return Word{}, false
			}
			args = args[values:]
		}
	}
	opts := tarExtractOptions.parse(args, true)
	if !extracts && !opts.flags["x"] && !opts.flags["--extract"] && !opts.flags["--get"] {
		return Word{}, false
	}
	for _, name := range []string{"C", "--directory"} {
		if v, ok := opts.values[name]; ok && v.Unquoted != "" {
			dir = v
		}
	}
	return dir, true
}

// unzipDirectory returns where unzip extracts to (-d, or the directory it runs in), and ok only
// when it extracts: -l, -t, -v and -Z list or test, and -p and -c write to standard output.
func unzipDirectory(args []Word) (Word, bool) {
	dir := Word{Unquoted: "."}
	for i := 0; i < len(args); i++ {
		switch t := args[i].Unquoted; {
		case t == "-d":
			if i+1 < len(args) {
				i++
				dir = args[i]
			}
		case isOption(args[i]) && strings.ContainsAny(t[1:], "ltvZpc"):
			return Word{}, false
		}
	}
	return dir, true
}

// cloneDirectory returns where a clone lands: its second operand, or a directory named after
// the repository in the one the command runs in.
func cloneDirectory(operands []Word) (Word, bool) {
	switch len(operands) {
	case 0:
		return Word{}, false
	case 1:
		repo := strings.TrimSuffix(strings.TrimRight(operands[0].Unquoted, "/"), ".git")
		if i := strings.LastIndexAny(repo, "/:"); i >= 0 {
			repo = repo[i+1:]
		}
		if repo == "" {
			return Word{}, false
		}
		return Word{Unquoted: repo}, true
	}
	return operands[1], true
}

// outputFile is the effect of writing the file w names, unless it names none: standard output
// (-) or a device. under is the directory a relative name is put in, when the command names one.
func outputFile(w Word, under *Word, written bool) (FileEffect, bool) {
	p, ok := wordPath(w, written)
	if !ok || p == "" || p == "-" || strings.HasPrefix(p, "/dev/") {
		return FileEffect{}, false
	}
	if under != nil && !path.IsAbs(p) {
		dir, ok := wordPath(*under, written)
		if !ok {
			return FileEffect{}, false
		}
		p = path.Join(dir, p)
	}
	return FileEffect{Path: p, Op: OpWrite}, true
}

// urlFileName is the name a download is saved under when the command takes it from the URL:
// what follows the last slash of its path. ok is false for a word that is no URL or names none.
func urlFileName(w Word, written bool) (Word, bool) {
	u, ok := wordPath(w, written)
	if !ok {
		return Word{}, false
	}
	scheme := strings.Index(u, "://")
	if scheme < 0 {
		return Word{}, false
	}
	rest := u[scheme+3:]
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	i := strings.LastIndexByte(rest, '/')
	if i < 0 || i+1 == len(rest) {
		return Word{}, false
	}
	name := rest[i+1:]
	return Word{Text: name, Unquoted: name, Known: w.Known}, true
}

// optionValue is the value of an option written in the word at i: attached to it when rest is
// not empty, and otherwise the next word. It returns the index of the last word read.
func optionValue(args []Word, i int, rest string) (Word, int, bool) {
	switch {
	case rest != "":
		return attachedValue(args[i], rest), i, true
	case i+1 < len(args):
		return args[i+1], i + 1, true
	}
	return Word{}, i, false
}

// outputFiles are the effects of the words of outputs that name a file, each under dir.
func outputFiles(outputs []Word, dir *Word, written bool) []FileEffect {
	var effects []FileEffect
	for _, w := range outputs {
		if e, ok := outputFile(w, dir, written); ok {
			effects = append(effects, e)
		}
	}
	return effects
}

// curlOutputs returns the files curl saves: the file of each -o/--output, and with
// -O/--remote-name the name each URL ends in, both under --output-dir when it is given.
func curlOutputs(args []Word, written bool) []FileEffect {
	var outputs, urls []Word
	var dir *Word
	remote := false
	for i := 0; i < len(args); i++ {
		t := args[i].Unquoted
		if len(t) < 2 || t[0] != '-' {
			urls = append(urls, args[i])
			continue
		}
		if t[1] == '-' {
			name, attached, _ := strings.Cut(t, "=")
			switch name {
			case "--remote-name", "--remote-name-all":
				remote = true
			case "--output", "--output-dir":
				value, last, ok := optionValue(args, i, attached)
				if !ok {
					return outputFiles(outputs, dir, written)
				}
				i = last
				if name == "--output-dir" {
					dir = &value
				} else {
					outputs = append(outputs, value)
				}
			case "--proxy", "--preproxy", "--referer", "--doh-url":
				// The address of a proxy or a referring page is no download.
				if attached == "" {
					i++
				}
			}
			continue
		}
		j := 1
		for j < len(t) && t[j] != '$' && t[j] != '`' && strings.IndexByte(curlValueOptions, t[j]) < 0 {
			remote = remote || t[j] == 'O'
			j++
		}
		if j == len(t) || t[j] == '$' || t[j] == '`' {
			continue // only options without a value
		}
		value, last, ok := optionValue(args, i, t[j+1:])
		if !ok {
			break
		}
		i = last
		if t[j] == 'o' {
			outputs = append(outputs, value)
		}
	}
	if remote {
		for _, u := range urls {
			if name, ok := urlFileName(u, written); ok {
				outputs = append(outputs, name)
			}
		}
	}
	return outputFiles(outputs, dir, written)
}

// wgetOutputs returns the files wget saves: the file of -O/--output-document, or else the name
// each URL ends in, under -P/--directory-prefix when it is given. A recursive download and
// --spider name none.
func wgetOutputs(args []Word, written bool) []FileEffect {
	var urls []Word
	var output, dir *Word
	named := true
	for i := 0; i < len(args); i++ {
		t := args[i].Unquoted
		if len(t) < 2 || t[0] != '-' {
			urls = append(urls, args[i])
			continue
		}
		if t[1] == '-' {
			name, attached, _ := strings.Cut(t, "=")
			switch name {
			case "--recursive", "--mirror", "--spider", "--page-requisites":
				named = false
			case "--output-document", "--directory-prefix":
				value, last, ok := optionValue(args, i, attached)
				if !ok {
					return nil
				}
				i = last
				if name == "--output-document" {
					output = &value
				} else {
					dir = &value
				}
			}
			continue
		}
		j := 1
		for j < len(t) && t[j] != '$' && t[j] != '`' && strings.IndexByte(wgetValueOptions, t[j]) < 0 {
			if t[j] == 'n' {
				j += 2 // -nv, -nc, -np, -nH: one option of two letters
				continue
			}
			named = named && t[j] != 'r' && t[j] != 'm' && t[j] != 'p'
			j++
		}
		if j >= len(t) || t[j] == '$' || t[j] == '`' {
			continue
		}
		value, last, ok := optionValue(args, i, t[j+1:])
		if !ok {
			break
		}
		i = last
		switch t[j] {
		case 'O':
			output = &value
		case 'P':
			dir = &value
		}
	}
	var outputs []Word
	switch {
	case output != nil:
		// -O takes the whole download, and -P does not apply to it.
		outputs, dir = append(outputs, *output), nil
	case named:
		for _, u := range urls {
			if name, ok := urlFileName(u, written); ok {
				outputs = append(outputs, name)
			}
		}
	}
	return outputFiles(outputs, dir, written)
}

// ddOutput returns the file dd writes: its of= operand.
func ddOutput(args []Word, written bool) []FileEffect {
	for _, w := range args {
		if value, ok := strings.CutPrefix(w.Unquoted, "of="); ok {
			if e, ok := outputFile(attachedValue(w, value), nil, written); ok {
				return []FileEffect{e}
			}
		}
	}
	return nil
}

// copiedHere returns what scp or rsync writes on this host: nothing when its last operand is on
// another one, and otherwise that operand, or each source's base name under it when it is
// written as a directory or there are several sources, as cp does. An rsync source written
// with a trailing slash puts the directory's contents into the destination, which is then a
// directory whose new files are not named: only FileWrites (written) reports it.
func copiedHere(name string, args []Word, written bool) []FileEffect {
	operands := readers[name].files(args)
	if len(operands) < 2 {
		return nil
	}
	target := operands[len(operands)-1]
	if remotePath(target.Unquoted) || strings.Contains(target.Unquoted, "://") {
		return nil
	}
	dest, ok := wordPath(target, written)
	if !ok || dest == "" {
		return nil
	}
	sources := operands[:len(operands)-1]
	into := len(sources) > 1 || writtenAsDirectory(target, written)
	var effects []FileEffect
	add := func(p string) {
		if !slices.ContainsFunc(effects, func(e FileEffect) bool { return e.Path == p }) {
			effects = append(effects, FileEffect{Path: p, Op: OpWrite})
		}
	}
	for _, s := range sources {
		src := s.Unquoted
		if remotePath(src) {
			src = src[strings.IndexByte(src, ':')+1:]
		}
		switch {
		case name == "rsync" && strings.HasSuffix(src, "/"):
			if written {
				add(asDirectory(dest))
			}
		case !into:
			add(dest)
		case (s.Known || written) && namesFile(src):
			add(path.Join(dest, path.Base(src)))
		}
	}
	return effects
}
