package shellcmd

import (
	"slices"
	"strings"
)

// dataWords says which words of a command carry data it takes in: every word, or, for a command
// whose words are read by what they are for, only the ones listed by their index in Argv.
type dataWords struct {
	listed bool
	index  []int
}

func (d dataWords) holds(index int) bool {
	return !d.listed || slices.Contains(d.index, index)
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
	opts := r.parse(args)
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

	// gzipReader covers gzip, gunzip, zcat and the lzma family, whose option sets match.
	gzipReader = reader{
		optionTable: optionTable{
			values: optionSpec{short: "S", long: []string{"--suffix"}},
			flags: optionSpec{short: "cdflnNqrstvVh123456789", long: []string{
				"--stdout", "--to-stdout", "--decompress", "--uncompress", "--force", "--keep",
				"--list", "--license", "--no-name", "--name", "--quiet", "--recursive",
				"--rsyncable", "--test", "--verbose", "--version", "--help", "--fast", "--best",
			}},
		},
	}

	// xzReader covers xz, unxz and xzcat. -B and -M name the value that follows, as xz also
	// spells them -BLOCK-size and -MEM-limit. --files and --files0 are left out of values: their
	// bare form's value is an operand already, and inputs() adds the attached --files=f form.
	xzReader = reader{
		optionTable: optionTable{
			values: optionSpec{short: "TB", long: []string{
				"--threads", "--block-size", "--block-list", "--memory", "--mem-limit",
				"--memlimit-compress", "--memlimit-decompress", "--memlimit-mt-decompress",
				"--flush-timeout",
			}},
		},
	}

	// zstdReader covers zstd, unzstd and zstdcat. Its dictionary options are left out of values
	// on purpose: -D and --patch-from name a file zstd reads, the dictionary or the reference,
	// so their values are operands like any other file it takes in.
	zstdReader = reader{
		optionTable: optionTable{
			values: optionSpec{short: "oTMB", long: []string{
				"--output-file", "--threads", "--memory", "--block-size", "--rsyncable",
			}},
		},
	}

	brotliReader = reader{
		optionTable: optionTable{
			values: optionSpec{short: "oZ", long: []string{"--output", "--quality", "--dictionary"}},
		},
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
		"base32": {optionTable: optionTable{values: optionSpec{short: "wbo", long: []string{"--wrap", "--break", "--output"}}}},

		// The stream compressors and decompressors read their operands: the file compress turns
		// into an archive, and the archive decompress turns back. They are the pipe-reading
		// counterpart of the printers above, so a credential file compressed into a pipeline's
		// first stage is a read the egress rules can follow.
		"gzip":       gzipReader,
		"gunzip":     gzipReader,
		"zcat":       gzipReader,
		"lzma":       gzipReader,
		"unlzma":     gzipReader,
		"lzcat":      gzipReader,
		"bzip2":      {},
		"bunzip2":    {},
		"bzcat":      {},
		"xz":         xzReader,
		"unxz":       xzReader,
		"xzcat":      xzReader,
		"zstd":       zstdReader,
		"unzstd":     zstdReader,
		"zstdcat":    zstdReader,
		"lz4":        {},
		"unlz4":      {},
		"lz4cat":     {},
		"brotli":     brotliReader,
		"compress":   {optionTable: optionTable{values: optionSpec{short: "b"}}},
		"uncompress": {optionTable: optionTable{values: optionSpec{short: "b"}}},

		"grep": grepReader,
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

	// tarOptions are tar's options that take a value, and the ones that say what it does: c, r
	// and u read the files named (tarFiles), and x extracts (tarExtractDirectory).
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
		flags: optionSpec{short: "crux", long: []string{"--create", "--append", "--update", "--extract", "--get"}},
	}

	ghGistCreateOptions = optionTable{values: optionSpec{short: "df", long: []string{"--desc", "--filename"}}}
)

// inputs returns the files the command's own words name as its input, and which of its words
// carry data. name and args are what the command runs (Command.program). An interpreter's
// inline script is read by scriptReads instead.
func (c *Command) inputs(name string, args []Word) (files []string, data dataWords) {
	if name == "" {
		return nil, data
	}
	// base is the index in Argv of the first of args.
	base := len(c.Argv) - len(args)
	switch name {
	case "sed":
		// In place, sed writes the files it reads back, which is an edit, not a read.
		if files, inPlace := scriptFiles(name, args); !inPlace {
			return unquoted(files), data
		}
	case "jq":
		return unquoted(jqFiles(args)), data
	case "cp", "install":
		if p, ok := copyOperands(name, args); ok {
			return unquoted(p.sources), data
		}
	case "scp", "rsync":
		operands := readers[name].files(args)
		if len(operands) < 2 {
			return nil, data
		}
		for _, source := range operands[:len(operands)-1] {
			if !remotePath(source.Unquoted) {
				files = append(files, source.Unquoted)
			}
		}
		return files, data
	case "tar":
		return unquoted(tarFiles(args)), data
	case "zip":
		return unquoted(zipFiles(args)), data
	case "curl":
		return curlInputs(args, base)
	case "wget":
		return wgetInputs(args, base)
	case "http", "https", "httpie", "xh", "xhs":
		return httpieInputs(args, base)
	case "gh":
		if isSub(args, "gist", "create") {
			return unquoted(ghGistCreateOptions.parse(args[2:]).operands), data
		}
	case "socat":
		return socatFiles(args), data
	case "xz", "unxz", "xzcat":
		files = unquoted(readers[name].files(args))
		// --files[=f] and --files0[=f] name a file whose lines are the paths xz compresses or
		// decompresses. The bare form's value is already an operand; the attached form's value
		// is not, and is added here.
		for _, w := range args {
			t := w.Unquoted
			if t == "--" {
				break
			}
			for _, p := range []string{"--files0=", "--files="} {
				if strings.HasPrefix(t, p) && len(t) > len(p) && !slices.Contains(files, t[len(p):]) {
					files = append(files, t[len(p):])
				}
			}
		}
		return files, data
	default:
		r, ok := readers[name]
		if !ok {
			return nil, data
		}
		operands := r.files(args)
		switch {
		case name == "xxd" && len(operands) > 1:
			operands = operands[:1]
		case name == "awk":
			operands = withoutAssignments(operands)
		case name == "less" || name == "more" || name == "tail":
			operands = withoutPlusWords(operands)
		}
		return unquoted(operands), data
	}
	return nil, data
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

// remotePath reports whether an scp or rsync operand names a file on another host: host:path,
// user@host:path, host::module or a URL.
func remotePath(operand string) bool {
	i := strings.IndexByte(operand, ':')
	return i > 0 && !strings.Contains(operand[:i], "/")
}

// tarFiles returns the files tar puts into an archive: its operands when it creates, appends or
// updates (c, r, u), and none when it extracts, lists or compares.
func tarFiles(args []Word) []Word {
	letters, _, args, ok := tarBundle(args)
	if !ok {
		return nil
	}
	opts := tarOptions.parse(args)
	reads := strings.ContainsAny(letters, "cru")
	for _, mode := range []string{"c", "r", "u", "--create", "--append", "--update"} {
		reads = reads || opts.flags[mode]
	}
	if !reads {
		return nil
	}
	return opts.operands
}

// tarBundle reads tar's first word when it is option letters without a dash (tar czf out.tgz
// src): the letters, the value each letter that takes one takes, which is the next word in
// order, and the words after those. When the first word is no bundle, letters is "" and rest is
// args. ok is false when the letters take more words than follow them.
func tarBundle(args []Word) (letters string, values map[byte]Word, rest []Word, ok bool) {
	if len(args) == 0 || args[0].Unquoted == "" || !isLetters(args[0].Unquoted) {
		return "", nil, args, true
	}
	letters, next := args[0].Unquoted, 1
	for i := 0; i < len(letters); i++ {
		if strings.IndexByte(tarOptions.values.short, letters[i]) < 0 {
			continue
		}
		if next >= len(args) {
			return "", nil, nil, false
		}
		if values == nil {
			values = map[byte]Word{}
		}
		values[letters[i]] = args[next]
		next++
	}
	return letters, values, args[next:], true
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
// filter came in with -f, and the files --rawfile and --slurpfile load into a variable. The
// operands after --args and --jsonargs are values, not files.
func jqFiles(args []Word) []Word {
	scripted := false
	var operands, loaded []Word
scan:
	for i := 0; i < len(args); i++ {
		switch t := args[i].Unquoted; {
		case t == "--":
			operands = append(operands, args[i+1:]...)
			break scan
		case t == "--args" || t == "--jsonargs":
			// What follows is the filter, if it has not come yet, and then values.
			if !scripted && len(operands) == 0 {
				return loaded
			}
			break scan
		case t == "--from-file":
			scripted = true
			i++
		case strings.HasPrefix(t, "--from-file="):
			scripted = true
		case t == "--arg" || t == "--argjson":
			i += 2
		case t == "--slurpfile" || t == "--rawfile":
			if i+2 < len(args) {
				loaded = append(loaded, args[i+2])
			}
			i += 2
		case t == "--indent" || t == "--library-path":
			i++
		case strings.HasPrefix(t, "--"):
		case isOption(args[i]):
			for j := 1; j < len(t); j++ {
				if !args[i].Known && (t[j] == '$' || t[j] == '`') {
					break // what follows in an unknown word is not option letters
				}
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
	return append(operands, loaded...)
}

// curlInputs returns the files curl sends and the words whose value it sends as the request body
// (readCurl). The files are the @file of -d, --data, --data-binary, --data-ascii,
// --data-urlencode and --json, the @file or <file of a -F field, and the file of -T. The words
// are the values of those options where they are content rather than a file, and of --data-raw
// and --form-string. A header, a URL, a credential and an output file are neither.
func curlInputs(args []Word, base int) (files []string, data dataWords) {
	data.listed = true
	for _, s := range readCurl(args).sends {
		value, file := s.value.Unquoted, ""
		switch s.kind {
		case curlData:
			if strings.HasPrefix(value, "@") {
				file = value[1:]
			}
		case curlURLEncode:
			// name@file and @file read a file; content, =content and name=content do not.
			if at := strings.IndexAny(value, "=@"); at >= 0 && value[at] == '@' {
				file = value[at+1:]
			}
		case curlForm:
			file = formFile(value)
		case curlUpload:
			if file = value; file == "." {
				continue
			}
		}
		// Content, or a file that is the output of a process substitution, is the word's own value.
		if file == "" || strings.HasPrefix(file, "<(") {
			data.index = append(data.index, base+s.index)
			continue
		}
		files = append(files, file)
	}
	return files, data
}

// wgetInputs returns the files wget sends (--post-file, --body-file) and the words whose value it
// sends as the request body (--post-data, --body-data), as readWget reads them.
func wgetInputs(args []Word, base int) (files []string, data dataWords) {
	data.listed = true
	for _, p := range readWget(args).posts {
		if p.file {
			files = append(files, p.value.Unquoted)
		} else {
			data.index = append(data.index, base+p.index)
		}
	}
	return files, data
}

// httpieInputs returns the files an HTTPie or xh request sends and the words whose value it sends
// as request data. A request item is read by the first separator in it: name@file uploads a file,
// name=@file and name:=@file take a field from one, and @file is the body; name=value and
// name:=json are data. Name:value is a header and name==value a URL parameter: neither is data,
// and nor is the method or the URL.
func httpieInputs(args []Word, base int) (files []string, data dataWords) {
	data.listed = true
	method, url := false, false
	for i, w := range args {
		t := w.Unquoted
		if isOption(w) {
			continue
		}
		// The first operand is the URL, or the method before it, which is letters alone; the
		// operand after a method is the URL whatever it is (http POST localhost). The request
		// items follow.
		if !url {
			if !method && isLetters(t) {
				method = true
			} else {
				url = true
			}
			continue
		}
		at := strings.IndexAny(t, ":=@;")
		if at < 0 {
			continue
		}
		rest := t[at+1:]
		switch t[at] {
		case ':':
			if !strings.HasPrefix(rest, "=") {
				continue // a header, or a URL
			}
			rest = rest[1:]
		case '=':
			if strings.HasPrefix(rest, "=") {
				continue // a URL parameter
			}
		case '@':
			file, _, _ := strings.Cut(rest, ";")
			files = append(files, file)
			continue
		default:
			continue // `Header;` sends an empty header
		}
		if strings.HasPrefix(rest, "@") {
			files = append(files, rest[1:])
		} else {
			data.index = append(data.index, base+i)
		}
	}
	return files, data
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

// socatFiles returns the files socat reads as an address: OPEN:path, FILE:path, GOPEN:path. Under
// -u socat reads only its first address and writes the second, under -U the reverse, and an
// address opened wronly or trunc is only written.
func socatFiles(args []Word) []string {
	var files []string
	read, address := 0, 0 // read is the one address read under -u or -U, 0 when both are
	for i := 0; i < len(args); i++ {
		t := args[i].Unquoted
		if address == 0 && isOption(args[i]) {
			switch t {
			case "-u":
				read = 1
			case "-U":
				read = 2
			case "-lf":
				i++ // the log file
			}
			continue
		}
		address++
		if read != 0 && address != read {
			continue
		}
		kind, rest, ok := strings.Cut(t, ":")
		if !ok || !strings.EqualFold(kind, "OPEN") && !strings.EqualFold(kind, "FILE") && !strings.EqualFold(kind, "GOPEN") {
			continue
		}
		path, options, _ := strings.Cut(rest, ",")
		written := false
		for _, o := range strings.Split(options, ",") {
			name, _, _ := strings.Cut(o, "=")
			written = written || strings.EqualFold(name, "wronly") || strings.EqualFold(name, "trunc")
		}
		if !written {
			files = append(files, path)
		}
	}
	return files
}
