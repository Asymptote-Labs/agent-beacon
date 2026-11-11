package shellcmd

import (
	"slices"
	"strings"
)

// FileReads returns the files every command reads, in order.
func FileReads(commands []Command) []string {
	var reads []string
	for _, c := range commands {
		reads = append(reads, c.FileReads()...)
	}
	return reads
}

// FileReads returns the files the command evidently takes as input: the targets of its input
// redirections, then the files its own words name -- what cat, head, base64 and the other printers
// print, what grep, rg, awk, sed and jq search or filter, the sources of cp, install, scp and
// rsync, what tar and zip put into an archive, and what curl, wget, `gh gist create` and socat
// send. A command it does not know contributes only its redirections, and so does a wrapper that
// moves the command (sudo -i).
//
// A path is as written (Word.Unquoted), also where it depends on something this package does not
// evaluate: ~/.aws/credentials, $HOME/.netrc and .env* are reported as those words. That differs
// from FileEffects, which leaves such a path out. An effect is taken as the exact file a command
// wrote; a read is asked what it names, and ~/.ssh/id_rsa names a private key whatever the home
// directory is. Like an effect, a relative path is relative to the directory the command runs in,
// which after a cd (AfterCD) is not the one the command line started in.
//
// Standard input (-), a device (/dev/stdin), a file on another host (scp host:path), a file a
// command edits in place (sed -i, which FileEffects reports) and a file a command names without
// taking it as input (a search pattern, an archive's exclusions, an identity file) are not reads.
func (c Command) FileReads() []string {
	var reads []string
	for _, r := range c.Redirects {
		if r.Op == "<" || r.Op == "<>" {
			reads = append(reads, r.Target.Unquoted)
		}
	}
	reads = append(reads, c.inputFiles()...)
	kept := reads[:0]
	for _, path := range reads {
		if path != "" && path != "-" && !strings.HasPrefix(path, "/dev/") {
			kept = append(kept, path)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// reader is how a command that reads its operands is read.
type reader struct {
	optionTable
	// script are the options that bring the command's pattern, program or filter, or make it need
	// none. A command that has them takes its first operand as that when none of them is given.
	script []string
}

// files returns the operands the command reads.
func (r reader) files(args []Word) []Word {
	opts := r.parse(args, true)
	files := opts.operands
	if r.script == nil || len(files) == 0 {
		return files
	}
	for _, option := range r.script {
		if _, ok := opts.values[option]; ok || opts.flags[option] {
			return files
		}
	}
	return files[1:]
}

var (
	grepReader = reader{
		optionTable: optionTable{values: optionSpec{short: "efmABCdD", long: []string{
			"--regexp", "--file", "--max-count", "--after-context", "--before-context", "--context",
			"--directories", "--devices", "--include", "--exclude", "--exclude-from", "--exclude-dir",
			"--label", "--binary-files",
		}}},
		script: []string{"e", "f", "--regexp", "--file"},
	}

	// readers are the commands whose operands are the files they read. The options listed are the
	// ones that take a value, so that the value is not taken for a file.
	readers = map[string]reader{
		"cat":  {},
		"tac":  {optionTable: optionTable{values: optionSpec{short: "s", long: []string{"--separator"}}}},
		"head": {optionTable: optionTable{values: optionSpec{short: "nc", long: []string{"--lines", "--bytes"}}}},
		"tail": {optionTable: optionTable{values: optionSpec{short: "ncs", long: []string{
			"--lines", "--bytes", "--pid", "--sleep-interval", "--max-unchanged-stats",
		}}}},
		"less": {optionTable: optionTable{values: optionSpec{short: "bhjkoOpPtTxyz"}}},
		"more": {optionTable: optionTable{values: optionSpec{short: "n", long: []string{"--lines"}}}},
		"bat": {optionTable: optionTable{values: optionSpec{short: "lHrm", long: []string{
			"--language", "--highlight-line", "--line-range", "--map-syntax", "--theme", "--style",
			"--color", "--decorations", "--paging", "--pager", "--wrap", "--tabs", "--terminal-width",
			"--file-name", "--diff-context", "--italic-text", "--ignored-suffix",
		}}}},
		"nl": {optionTable: optionTable{values: optionSpec{short: "bdfhilnsvw", long: []string{
			"--body-numbering", "--section-delimiter", "--footer-numbering", "--header-numbering",
			"--line-increment", "--join-blank-lines", "--number-format", "--number-separator",
			"--starting-line-number", "--number-width",
		}}}},
		"strings": {optionTable: optionTable{values: optionSpec{short: "nteTs", long: []string{
			"--bytes", "--radix", "--encoding", "--target", "--output-separator",
		}}}},
		// xxd's second operand is the file it writes.
		"xxd": {optionTable: optionTable{values: optionSpec{short: "cglnosR"}}},
		"od": {optionTable: optionTable{values: optionSpec{short: "AjNSt", long: []string{
			"--address-radix", "--skip-bytes", "--read-bytes", "--format", "--endian",
		}}}},
		"hexdump": {optionTable: optionTable{values: optionSpec{short: "nsef", long: []string{
			"--length", "--skip", "--format", "--format-file",
		}}}},
		// GNU -i ignores garbage and BSD -i names the input: either way the file is an operand.
		"base64": {optionTable: optionTable{values: optionSpec{short: "wbo", long: []string{"--wrap", "--break", "--output"}}}},

		"grep":  grepReader,
		"egrep": grepReader,
		"fgrep": grepReader,
		"rg": {
			optionTable: optionTable{
				values: optionSpec{short: "efgtTmABCjMEdr", long: []string{
					"--regexp", "--file", "--glob", "--iglob", "--type", "--type-not", "--type-add",
					"--type-clear", "--max-count", "--after-context", "--before-context", "--context",
					"--threads", "--max-columns", "--encoding", "--max-depth", "--replace", "--sort",
					"--sortr", "--color", "--colors", "--context-separator", "--field-match-separator",
					"--field-context-separator", "--path-separator", "--pre", "--pre-glob",
					"--regex-size-limit", "--dfa-size-limit", "--max-filesize", "--ignore-file", "--engine",
				}},
				flags: optionSpec{long: []string{"--files", "--type-list"}},
			},
			script: []string{"e", "f", "--regexp", "--file", "--files", "--type-list"},
		},
		"awk": {
			optionTable: optionTable{values: optionSpec{short: "FvfeilE", long: []string{
				"--field-separator", "--assign", "--file", "--source", "--include", "--load", "--exec",
			}}},
			script: []string{"f", "e", "E", "--file", "--source", "--exec"},
		},

		// The last operand of scp and rsync is where the files go.
		"scp": {optionTable: optionTable{values: optionSpec{short: "cDFiJloPSX"}}},
		"rsync": {optionTable: optionTable{values: optionSpec{short: "BMTef", long: []string{
			"--rsh", "--rsync-path", "--password-file", "--exclude", "--include", "--exclude-from",
			"--include-from", "--files-from", "--filter", "--block-size", "--temp-dir", "--remote-option",
			"--max-size", "--min-size", "--port", "--log-file", "--log-file-format", "--out-format",
			"--chmod", "--chown", "--partial-dir", "--backup-dir", "--suffix", "--compare-dest",
			"--copy-dest", "--link-dest", "--timeout", "--contimeout", "--bwlimit", "--info", "--debug",
			"--compress-level", "--compress-choice", "--skip-compress", "--address", "--sockopts",
			"--iconv", "--checksum-choice", "--usermap", "--groupmap", "--max-delete", "--modify-window",
			"--write-batch", "--only-write-batch", "--read-batch", "--protocol", "--checksum-seed",
		}}}},
	}

	// tarOptions are tar's options that take a value, and the ones that say what it does: only c,
	// r and u read the files named.
	tarOptions = optionTable{
		values: optionSpec{short: "bCfFgHIKLNTVX", long: []string{
			"--file", "--directory", "--exclude", "--exclude-from", "--files-from", "--transform",
			"--xform", "--strip-components", "--use-compress-program", "--owner", "--group", "--mode",
			"--mtime", "--newer", "--after-date", "--newer-mtime", "--format", "--blocking-factor",
			"--record-size", "--label", "--tape-length", "--listed-incremental", "--starting-file",
			"--info-script", "--new-volume-script", "--checkpoint-action", "--exclude-tag",
			"--exclude-tag-all", "--exclude-tag-under", "--sort", "--pax-option", "--level", "--suffix",
			"--warning", "--quoting-style", "--rmt-command", "--rsh-command", "--volno-file",
			"--index-file", "--owner-map", "--group-map", "--hole-detection",
		}},
		flags: optionSpec{short: "cru", long: []string{"--create", "--append", "--update"}},
	}

	ghGistCreateOptions = optionTable{values: optionSpec{short: "df", long: []string{"--desc", "--filename"}}}

	wgetOptions = optionTable{values: optionSpec{long: []string{"--post-file", "--body-file"}}}
)

// inputFiles returns the files the command's own words name as its input.
func (c Command) inputFiles() []string {
	name := c.Name()
	if name == "" {
		return nil
	}
	words, _ := c.Unwrapped()
	args := words[1:]
	switch name {
	case "sed", "gsed":
		// In place, sed writes the files it reads back, which is an edit, not a read.
		if files, inPlace := scriptFiles(name, args); !inPlace {
			return unquoted(files)
		}
	case "jq":
		return unquoted(jqFiles(args))
	case "cp", "install":
		return unquoted(copySources(name, args))
	case "scp", "rsync":
		operands := readers[name].files(args)
		if len(operands) < 2 {
			return nil
		}
		var local []string
		for _, source := range operands[:len(operands)-1] {
			if !remotePath(source.Unquoted) {
				local = append(local, source.Unquoted)
			}
		}
		return local
	case "tar":
		return unquoted(tarFiles(args))
	case "zip":
		return unquoted(zipFiles(args))
	case "curl":
		return curlFiles(args)
	case "wget":
		opts := wgetOptions.parse(args, true)
		var files []string
		for _, option := range wgetOptions.values.long {
			if file, ok := opts.values[option]; ok {
				files = append(files, file.Unquoted)
			}
		}
		return files
	case "gh":
		if c.Is("gh", "gist", "create") {
			return unquoted(ghGistCreateOptions.parse(args[2:], true).operands)
		}
	case "socat":
		return socatFiles(args)
	default:
		r, ok := readers[name]
		if !ok {
			return nil
		}
		files := r.files(args)
		switch {
		case name == "xxd" && len(files) > 1:
			files = files[:1]
		case name == "awk":
			files = withoutAssignments(files)
		case name == "less" || name == "more" || name == "tail":
			files = withoutPlusWords(files)
		}
		return unquoted(files)
	}
	return nil
}

// unquoted are words as written.
func unquoted(words []Word) []string {
	if len(words) == 0 {
		return nil
	}
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w.Unquoted
	}
	return out
}

// withoutAssignments leaves out awk's var=value operands.
func withoutAssignments(words []Word) []Word {
	var files []Word
	for _, w := range words {
		if name, _, ok := strings.Cut(w.Unquoted, "="); ok && isShellName(name) {
			continue
		}
		files = append(files, w)
	}
	return files
}

// withoutPlusWords leaves out the words less, more and tail take as a command or a line number
// rather than a file: +G, +/pattern, +5.
func withoutPlusWords(words []Word) []Word {
	var files []Word
	for _, w := range words {
		if !strings.HasPrefix(w.Unquoted, "+") {
			files = append(files, w)
		}
	}
	return files
}

// copySources returns what cp or install copies: every operand with -t, every operand but the
// last, the destination, otherwise.
func copySources(name string, args []Word) []Word {
	opts := optionTable{valueOptions[name], flagOptions[name], longOptions[name]}.parse(args, true)
	if opts.flags["d"] || opts.flags["--directory"] {
		return nil // install -d creates its operands as directories
	}
	sources := opts.operands
	if _, ok := opts.values["t"]; ok {
		return sources
	}
	if _, ok := opts.values["--target-directory"]; ok {
		return sources
	}
	if len(sources) < 2 {
		return nil
	}
	return sources[:len(sources)-1]
}

// remotePath reports whether an scp or rsync operand names a file on another host: host:path,
// user@host:path, host::module or a URL.
func remotePath(operand string) bool {
	i := strings.IndexByte(operand, ':')
	return i > 0 && !strings.Contains(operand[:i], "/")
}

// tarFiles returns the files tar puts into an archive: its operands when it creates, appends or
// updates (c, r, u), and none when it extracts, lists or compares.
func tarFiles(args []Word) []Word {
	reads := false
	// The first word may be option letters without a dash (tar czf out.tgz src): each letter that
	// takes a value takes the next word, in order.
	if len(args) > 0 {
		if bundle := args[0].Unquoted; bundle != "" && isLetters(bundle) {
			values := 1
			for i := 0; i < len(bundle); i++ {
				if strings.IndexByte(tarOptions.flags.short, bundle[i]) >= 0 {
					reads = true
				}
				if strings.IndexByte(tarOptions.values.short, bundle[i]) >= 0 {
					values++
				}
			}
			if values > len(args) {
				return nil
			}
			args = args[values:]
		}
	}
	opts := tarOptions.parse(args, true)
	for _, mode := range []string{"c", "r", "u", "--create", "--append", "--update"} {
		reads = reads || opts.flags[mode]
	}
	if !reads {
		return nil
	}
	return opts.operands
}

func isLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i] | 0x20; c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// zipValueOptions are zip's options that take the next word as their value.
var zipValueOptions = []string{
	"-b", "-t", "-tt", "-n", "-P", "-O", "-TT", "-s", "-ds", "-lf", "--temp-path", "--from-date",
	"--before-date", "--suffixes", "--password", "--output-file", "--unzip-command", "--split-size",
	"--dot-size", "--logfile-path",
}

// zipFiles returns the files zip puts into an archive: the operands after the archive's name,
// and the names after -i. The names after -x are the ones it leaves out.
func zipFiles(args []Word) []Word {
	var files []Word
	archive := false
	for i := 0; i < len(args); i++ {
		switch t := args[i].Unquoted; {
		case t == "-x" || t == "--exclude":
			for i+1 < len(args) && !isOption(args[i+1]) {
				i++
			}
		case slices.Contains(zipValueOptions, t):
			i++
		case isOption(args[i]):
		case !archive:
			archive = true
		default:
			files = append(files, args[i])
		}
	}
	return files
}

// isOption reports whether a word is written as an option.
func isOption(w Word) bool {
	return len(w.Unquoted) > 1 && w.Unquoted[0] == '-'
}

// jqFiles returns the files jq reads: its operands after the filter, or all of them when the
// filter came in with -f. The operands after --args and --jsonargs are values, not files.
func jqFiles(args []Word) []Word {
	scripted := false
	var operands []Word
scan:
	for i := 0; i < len(args); i++ {
		switch t := args[i].Unquoted; {
		case t == "--":
			operands = append(operands, args[i+1:]...)
			break scan
		case t == "--args" || t == "--jsonargs":
			// What follows is the filter, if it has not come yet, and then values.
			if !scripted && len(operands) == 0 {
				return nil
			}
			break scan
		case t == "--from-file":
			scripted = true
			i++
		case strings.HasPrefix(t, "--from-file="):
			scripted = true
		case t == "--arg" || t == "--argjson" || t == "--slurpfile" || t == "--rawfile":
			i += 2
		case t == "--indent" || t == "--library-path":
			i++
		case strings.HasPrefix(t, "--"):
		case isOption(args[i]):
			for j := 1; j < len(t); j++ {
				if t[j] == 'f' {
					scripted = true
				}
				if t[j] == 'f' || t[j] == 'L' {
					if j+1 == len(t) {
						i++ // the file or directory is the next word
					}
					break
				}
			}
		default:
			operands = append(operands, args[i])
		}
	}
	if !scripted && len(operands) > 0 {
		operands = operands[1:]
	}
	return operands
}

// curlValueOptions are curl's short options that take a value.
const curlValueOptions = "AbcCdDeEFHKmoPQrtTuUwxXyYz"

// The ways a curl option's value names a file it sends.
const (
	curlData      = iota + 1 // @file
	curlURLEncode            // @file, name@file
	curlForm                 // name=@file, name=<file
	curlUpload               // file
)

// curlFiles returns the files curl sends: the @file of -d, --data, --data-binary, --data-ascii,
// --data-urlencode and --json, the @file or <file of a -F field, and the file of -T. The value of
// --data-raw and --form-string is sent as it is, and a header, a URL or an output file is no read.
func curlFiles(args []Word) []string {
	var files []string
	for i := 0; i < len(args); i++ {
		t := args[i].Unquoted
		if len(t) < 2 || t[0] != '-' {
			continue // a URL
		}
		var value string
		sends, attached := 0, false
		if t[1] == '-' {
			var name string
			name, value, attached = strings.Cut(t, "=")
			switch name {
			case "--data", "--data-binary", "--data-ascii", "--json":
				sends = curlData
			case "--data-urlencode":
				sends = curlURLEncode
			case "--form":
				sends = curlForm
			case "--upload-file":
				sends = curlUpload
			default:
				continue
			}
		} else {
			j := 1
			for j < len(t) && t[j] != '$' && t[j] != '`' && strings.IndexByte(curlValueOptions, t[j]) < 0 {
				j++
			}
			if j == len(t) || t[j] == '$' || t[j] == '`' {
				continue // only options without a value
			}
			switch t[j] {
			case 'd':
				sends = curlData
			case 'F':
				sends = curlForm
			case 'T':
				sends = curlUpload
			}
			value, attached = t[j+1:], j+1 < len(t)
		}
		if !attached {
			if i+1 == len(args) {
				break
			}
			i++
			value = args[i].Unquoted
		}
		switch sends {
		case curlData:
			if strings.HasPrefix(value, "@") {
				files = append(files, value[1:])
			}
		case curlURLEncode:
			// name@file and @file read a file; content, =content and name=content do not.
			if at := strings.IndexAny(value, "=@"); at >= 0 && value[at] == '@' {
				files = append(files, value[at+1:])
			}
		case curlForm:
			files = append(files, formFile(value))
		case curlUpload:
			if value != "." {
				files = append(files, value)
			}
		}
	}
	return files
}

// formFile returns the file a curl -F field reads (name=@file, name=<file), without the
// ;type= and ;filename= that may follow it, or "".
func formFile(field string) string {
	_, content, ok := strings.Cut(field, "=")
	if !ok || content == "" || content[0] != '@' && content[0] != '<' {
		return ""
	}
	content = content[1:]
	if strings.HasPrefix(content, `"`) {
		if end := strings.IndexByte(content[1:], '"'); end >= 0 {
			return content[1 : 1+end]
		}
	}
	file, _, _ := strings.Cut(content, ";")
	return file
}

// socatFiles returns the files socat opens as an address: OPEN:path, FILE:path, GOPEN:path.
func socatFiles(args []Word) []string {
	var files []string
	for _, w := range args {
		kind, rest, ok := strings.Cut(w.Unquoted, ":")
		if !ok || !strings.EqualFold(kind, "OPEN") && !strings.EqualFold(kind, "FILE") && !strings.EqualFold(kind, "GOPEN") {
			continue
		}
		path, _, _ := strings.Cut(rest, ",")
		files = append(files, path)
	}
	return files
}
