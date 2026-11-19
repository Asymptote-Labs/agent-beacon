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

// gitCloneOptions are git clone's options that take a value.
var gitCloneOptions = optionTable{values: optionSpec{short: "boucj", long: []string{
	"--branch", "--origin", "--upload-pack", "--config", "--jobs", "--reference", "--reference-if-able",
	"--separate-git-dir", "--depth", "--shallow-since", "--shallow-exclude", "--filter", "--template",
	"--server-option", "--bundle-uri", "--revision",
}}}

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
		dir, ok = cloneDirectory(gitCloneOptions.parse(args[1:]).operands)
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
	letters, values, args, ok := tarBundle(args)
	if !ok {
		return Word{}, false
	}
	opts := tarOptions.parse(args)
	if !strings.Contains(letters, "x") && !opts.flags["x"] && !opts.flags["--extract"] && !opts.flags["--get"] {
		return Word{}, false
	}
	dir := literalWord(".")
	if v, ok := values['C']; ok {
		dir = v
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
	dir := literalWord(".")
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
		return attachedValue(operands[0], repo), true
	}
	return operands[1], true
}

// outputFile is the write of the file w names, unless it names none: standard output (-) or a
// device. under is the directory a relative name is put in, when the command names one.
func outputFile(w Word, under *Word) (write, bool) {
	p, exact := wordPath(w)
	if !isFile(p) {
		return write{}, false
	}
	if under != nil && !path.IsAbs(p) {
		dir, dirExact := wordPath(*under)
		if dir == "" {
			return write{}, false
		}
		p, exact = path.Join(dir, p), exact && dirExact
	}
	return write{FileEffect: FileEffect{Path: p, Op: OpWrite}, exact: exact}, true
}

// urlFileName is the name a download is saved under when the command takes it from the URL:
// what follows the last slash of its path. ok is false for a word that is no URL or names none.
func urlFileName(w Word) (Word, bool) {
	u := w.Unquoted
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
	return attachedValue(w, rest[i+1:]), true
}

// outputFiles are the writes of the words of outputs that name a file, each under dir.
func outputFiles(outputs []Word, dir *Word) []write {
	var out []write
	for _, w := range outputs {
		if e, ok := outputFile(w, dir); ok {
			out = append(out, e)
		}
	}
	return out
}

// curlOutputs returns the files curl saves: the file of each -o/--output, and with
// -O/--remote-name the name each URL ends in, both under --output-dir when it is given.
func curlOutputs(args []Word) []write {
	a := readCurl(args)
	outputs := a.outputs
	if a.remote {
		for _, u := range a.urls {
			if name, ok := urlFileName(u); ok {
				outputs = append(outputs, name)
			}
		}
	}
	return outputFiles(outputs, a.dir)
}

// wgetOutputs returns the files wget saves: the file of -O/--output-document, or else the name
// each URL ends in, under -P/--directory-prefix when it is given. A recursive download and
// --spider name none.
func wgetOutputs(args []Word) []write {
	a := readWget(args)
	var outputs []Word
	switch {
	case a.output != nil:
		// -O takes the whole download, and -P does not apply to it.
		return outputFiles([]Word{*a.output}, nil)
	case a.named:
		for _, u := range a.urls {
			if name, ok := urlFileName(u); ok {
				outputs = append(outputs, name)
			}
		}
	}
	return outputFiles(outputs, a.dir)
}

// ddOutput returns the file dd writes: its of= operand.
func ddOutput(args []Word) []write {
	for _, w := range args {
		if value, ok := strings.CutPrefix(w.Unquoted, "of="); ok {
			if e, ok := outputFile(attachedValue(w, value), nil); ok {
				return []write{e}
			}
		}
	}
	return nil
}

// copiedHere returns what scp or rsync writes on this host: nothing when its last operand is on
// another one, and otherwise that operand, or each source's base name under it when it is
// written as a directory or there are several sources, as cp does. An rsync source written
// with a trailing slash puts the directory's contents into the destination, which is then a
// directory whose new files are not named: a write that is no effect.
func copiedHere(name string, args []Word) []write {
	operands := readers[name].files(args)
	if len(operands) < 2 {
		return nil
	}
	target := operands[len(operands)-1]
	if remotePath(target.Unquoted) || strings.Contains(target.Unquoted, "://") {
		return nil
	}
	dest, destExact := wordPath(target)
	if !isFile(dest) {
		return nil
	}
	sources := operands[:len(operands)-1]
	into := len(sources) > 1 || writtenAsDirectory(target)
	var out []write
	add := func(p string, exact bool) {
		if k := slices.IndexFunc(out, func(w write) bool { return w.Path == p }); k >= 0 {
			out[k].exact = out[k].exact || exact
			return
		}
		out = append(out, write{FileEffect: FileEffect{Path: p, Op: OpWrite}, exact: exact})
	}
	for _, s := range sources {
		src := s.Unquoted
		if remotePath(src) {
			src = src[strings.IndexByte(src, ':')+1:]
		}
		switch {
		case name == "rsync" && strings.HasSuffix(src, "/"):
			add(asDirectory(dest), false)
		case !into:
			add(dest, destExact)
		case namesFile(src):
			add(path.Join(dest, path.Base(src)), s.Known && destExact)
		}
	}
	return out
}
