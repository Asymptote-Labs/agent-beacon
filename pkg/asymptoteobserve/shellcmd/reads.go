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
)

// inputs returns the files the command's own words name as its input, and which of its words
// carry data. name and args are what the command runs (Command.run). An interpreter's inline
// script is read by scriptReads instead.
func (c *Command) inputs(name string, args []Word) (files []string, data dataWords) {
	if name == "" {
		return nil, data
	}
	// base is the index in Argv of the first of args.
	base := len(c.Argv) - len(args)
	switch name {
	case "sed", "gsed":
		// In place, sed writes the files it reads back, which is an edit, not a read.
		if files, inPlace := scriptFiles(name, args); !inPlace {
			return unquoted(files), data
		}
	case "jq":
		return unquoted(jqFiles(args)), data
	case "cp", "install":
		return unquoted(copySources(name, args)), data
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
		if len(args) >= 2 && args[0].Known && args[0].Text == "gist" && args[1].Known && args[1].Text == "create" {
			return unquoted(ghGistCreateOptions.parse(args[2:], true).operands), data
		}
	case "socat":
		return socatFiles(args), data
	case "openssl":
		return opensslInputs(args, base)
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

// curlValueOptions are curl's short options that take a value.
const curlValueOptions = "AbcCdDeEFHKmoPQrtTuUwxXyYz"

// What the value of a curl option is to the request body.
const (
	curlData      = iota + 1 // the body, or @file
	curlURLEncode            // content, name=content, @file or name@file
	curlForm                 // name=content, name=@file or name=<file
	curlRaw                  // the body as written
	curlUpload               // a file
)

// curlInputs returns the files curl sends and the words whose value it sends as the request body.
// The files are the @file of -d, --data, --data-binary, --data-ascii, --data-urlencode and --json,
// the @file or <file of a -F field, and the file of -T. The words are the values of those options
// where they are content rather than a file, and of --data-raw and --form-string. A header, a
// URL, a credential and an output file are neither.
func curlInputs(args []Word, base int) (files []string, data dataWords) {
	data.listed = true
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
			case "--data-raw", "--form-string":
				sends = curlRaw
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
		file := ""
		switch sends {
		case 0:
			continue
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
			data.index = append(data.index, base+i)
			continue
		}
		files = append(files, file)
	}
	return files, data
}

// wgetInputs returns the files wget sends (--post-file, --body-file) and the words whose value it
// sends as the request body (--post-data, --body-data).
func wgetInputs(args []Word, base int) (files []string, data dataWords) {
	data.listed = true
	for i := 0; i < len(args); i++ {
		name, value, attached := strings.Cut(args[i].Unquoted, "=")
		file := name == "--post-file" || name == "--body-file"
		if !file && name != "--post-data" && name != "--body-data" {
			continue
		}
		if !attached {
			if i+1 == len(args) {
				break
			}
			i++
			value = args[i].Unquoted
		}
		if file {
			files = append(files, value)
		} else {
			data.index = append(data.index, base+i)
		}
	}
	return files, data
}

// opensslFileOptions are the subcommand options whose value is a file openssl reads: the input
// of enc, dgst, rsa, pkey, x509, req and pkcs12 and their kin, the keys -inkey and -key, the
// certificates -certfile, -signkey, -recip and -content, the config -config, the CA bundle
// -CAfile and s_client's -verifyCAfile, -signaturefile of cms and smime, the randomness seeds
// -rand and -writerand, enc's -kfile and the PVK key -pvkfile. The option and its value may be
// attached (`-in=key.pem` or the legacy `-inkey.pem`); opensslInputs peels the longest known name.
var opensslFileOptions = []string{
	"-in", "-inkey", "-key", "-certfile", "-signkey", "-recip", "-content", "-config",
	"-CAfile", "-verifyCAfile", "-signaturefile", "-rand", "-writerand", "-kfile", "-pvkfile",
}

// opensslValueOptions are the options that consume a value openssl does not read as a file: the
// output file, the forms, the digest and cipher names, the password sources and the literal
// password of enc's -k, and s_client's connection options.
var opensslValueOptions = []string{
	"-out", "-inform", "-outform", "-inkeyform", "-keyform", "-certform", "-digest", "-cipher",
	"-md", "-kdf", "-iter", "-engine", "-provider", "-provider-path", "-saltlen", "-macalg",
	"-passin", "-passout", "-k", "-salt", "-connect", "-servername", "-proxy", "-passarg",
	"-passphrase-arg",
}

// opensslInputs returns the files openssl reads and the words carrying a literal password.
//
// The first word after the name is the subcommand (`openssl rsa -in key.pem ...`); the option
// names that hold files are shared by the subcommands, so one table covers them. Every other
// word that starts with `-` is an option and is skipped, since OpenSSL's option vocabulary is
// far larger than the ones it reads files by.
//
// -passin and -passout take a password source: `file:path` is a read, a `pass:...`, `env:...`
// or plain password is the word's own data, and a `stdin:` or `fd:` source is neither. The
// short form of enc's `-k`, whose value is a literal password, is data as well.
func opensslInputs(args []Word, base int) (files []string, data dataWords) {
	data.listed = true
	if len(args) < 2 || !args[0].Known || args[0].Text == "" {
		return nil, data
	}
	for i := 1; i < len(args); i++ {
		w := args[i]
		t := w.Unquoted
		if !isOption(w) {
			files = append(files, t)
			continue
		}
		name := strings.TrimPrefix(t, "-")
		value, attached := "", false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			value, attached = name[eq+1:], true
			name = name[:eq]
		}
		if !attached {
			if optionValue(name, opensslFileOptions) || optionValue(name, opensslValueOptions) {
				// The word is a whole known option; its value is the next word.
				if i+1 < len(args) {
					i++
					value = args[i].Unquoted
					attached = true
				}
			} else if option, rest, ok := opensslAttached(name); ok {
				// The legacy attached form: -inkey.pem names -inkey and its value .pem.
				name, value, attached = option, rest, true
			}
		}
		switch {
		case !attached:
		case optionValue(name, opensslFileOptions):
			if value != "-" {
				files = append(files, value)
			}
		case name == "passin" || name == "passout":
			files, data = opensslPasswordSource(value, base+i, files, data)
		case name == "k":
			data.index = append(data.index, base+i)
		}
	}
	return files, data
}

// optionValue reports whether a word is one of the option names that take a value.
func optionValue(name string, table []string) bool {
	return slices.Contains(table, "-"+name)
}

// opensslAttached splits the legacy attached spelling of an option and its value (-inkey.pem):
// the longest known option name the word begins, and the rest as the value.
func opensslAttached(name string) (string, string, bool) {
	best := ""
	for _, table := range [][]string{opensslFileOptions, opensslValueOptions} {
		for _, option := range table {
			option = option[1:] // the option without its dash
			if len(option) > len(best) && strings.HasPrefix(name, option) && len(option) < len(name) {
				best = option
			}
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, name[len(best):], true
}

// opensslPasswordSource reads one password source as openssl's -passin and -passout spell them.
func opensslPasswordSource(value string, index int, files []string, data dataWords) ([]string, dataWords) {
	scheme, rest, hasScheme := strings.Cut(value, ":")
	if !hasScheme {
		data.index = append(data.index, index)
		return files, data
	}
	switch scheme {
	case "file":
		if rest != "" {
			files = append(files, rest)
		}
	case "pass", "env":
		data.index = append(data.index, index)
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
