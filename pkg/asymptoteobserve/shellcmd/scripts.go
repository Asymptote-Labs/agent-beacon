package shellcmd

import (
	"slices"
	"strings"
)

// language is how FileReads reads a script an interpreter is given on its command line or its
// standard input: where the script is, which calls in it open a file for reading, and how it
// names what it read. It follows a call whose path is a string literal, outside strings and
// comments, and the names the script gives the file's content on its way to a call that sends
// it (scriptVars). It does not follow a path a script computes.
type language struct {
	// eval are the short options whose value is the script (python -c, node -e), values the ones
	// that take some other value, and module the ones that make the interpreter run something
	// that is not an inline script (python -m). evalLong are the long forms of eval, valuesLong
	// the long options that take some other value in the next word (node --require x), and
	// subcommand the word that takes the script instead (deno eval). wordValues is set when a
	// short option's value is always the next word and never the rest of its cluster: node's -pe
	// is -p and -e, and its script is the word after.
	eval, values, module string
	evalLong, valuesLong []string
	subcommand           string
	wordValues           bool
	// calls are the functions that open a file for reading, as written before the parenthesis.
	// A call written as a method (fs.readFileSync) matches by its last name, and a name with a
	// dot in it (File.read) only in full.
	calls []string
	// sends are the calls that send what they are given over the network, matched as calls are.
	// chain are the methods that do when called on what such a call returns: the end of
	// https.request(..).end(data). quiet are the names in such a call after which an argument is
	// a credential the call authenticates with, not data (auth=, CURLOPT_USERPWD); a header
	// whose name says it holds a credential (Authorization, Cookie, X-Api-Key) is one too
	// (isCredentialHeader). Other headers and URL parameters are data: what a script puts there
	// is sent.
	sends, chain, quiet []string
	// then, for a call it is set for, is what must follow the call for it to read the file:
	// Path("x") alone only names one.
	then map[string][]string
	// modes is set when a second string argument is the mode the file is opened in.
	modes bool
	// bare is set when a call may be written without parentheses (ruby: File.read ".env").
	bare bool
	// prefixes are the letters a string literal may begin with (python: r"..", b"..").
	prefixes string
	// backtick, triple, hash and slashes say which strings and comments the language has beyond
	// '..' and "..": `..`, '''..''' and """..""", # to the end of the line, and // to it.
	backtick, triple, hash, slashes bool
	// How the language names a value, beyond name = value. keywords: inside a call's
	// parentheses name=value is an argument, not an assignment (python). as: `with .. as name`
	// (python). arrows: the parameters of a callback, (a, b) => and function (a, b)
	// (javascript). blocks: the parameters of a block, { |a| and do |a| (ruby).
	keywords, as, arrows, blocks bool
}

var (
	python = language{
		eval: "c", values: "WXQ", module: "m",
		calls:    []string{"open", "Path"},
		sends:    []string{"post", "put", "patch", "request", "urlopen", "Request", "send", "sendall", "sendto"},
		quiet:    []string{"auth", "cert"},
		then:     map[string][]string{"Path": {".read_text", ".read_bytes", ".open"}},
		modes:    true,
		prefixes: "rbuRBU",
		triple:   true,
		hash:     true,
		keywords: true,
		as:       true,
	}
	javascript = language{
		eval: "ep", values: "r", evalLong: []string{"--eval", "--print"}, wordValues: true,
		valuesLong: []string{"--require", "--import", "--loader", "--experimental-loader", "--input-type", "--conditions"},
		calls: []string{
			"readFileSync", "readFile", "createReadStream", "readTextFileSync", "readTextFile", "Bun.file",
		},
		sends:    []string{"fetch", "axios", "post", "put", "patch", "request", "send", "sendBeacon", "connect", "createConnection"},
		chain:    []string{"end", "write"},
		quiet:    []string{"auth"},
		backtick: true,
		slashes:  true,
		arrows:   true,
	}
	deno = func() language {
		l := javascript
		l.eval, l.values, l.evalLong, l.subcommand = "", "", nil, "eval"
		return l
	}()
	ruby = language{
		eval: "e", values: "IrCEF",
		calls: []string{
			"File.read", "File.readlines", "File.binread", "File.foreach", "File.open",
			"IO.read", "IO.readlines", "IO.binread", "IO.foreach",
		},
		sends:  []string{"post", "post_form", "put", "patch", "TCPSocket.new", "TCPSocket.open"},
		chain:  []string{"write", "puts", "print", "send"},
		quiet:  []string{"basic_auth"},
		modes:  true,
		bare:   true,
		hash:   true,
		blocks: true,
	}
	php = language{
		eval: "r", values: "dcz",
		calls:   []string{"file_get_contents", "fopen", "readfile", "file"},
		sends:   []string{"curl_setopt", "curl_setopt_array", "stream_context_create", "socket_write", "socket_send", "mail"},
		quiet:   []string{"CURLOPT_USERPWD", "CURLOPT_XOAUTH2_BEARER"},
		modes:   true,
		hash:    true,
		slashes: true,
	}
)

// scriptLanguage returns the language of the interpreter a command runs, by its base name.
func scriptLanguage(name string) (*language, bool) {
	switch {
	case strings.HasPrefix(name, "python"), strings.HasPrefix(name, "pypy"):
		return &python, true
	case name == "node", name == "nodejs", name == "bun":
		return &javascript, true
	case name == "deno":
		return &deno, true
	case name == "ruby":
		return &ruby, true
	case name == "php":
		return &php, true
	}
	return nil, false
}

// inlineScript returns the script the interpreter is given as text: the value of its eval option,
// or, when it is given no script file, the here-document or here-string on its standard input.
// The text is as written, so a part the shell would expand is still its source. It is "" when the
// interpreter runs a file, a module, or what a pipe sends it.
func inlineScript(l *language, c *Command, args []Word) string {
	for i := 0; i < len(args); i++ {
		t := args[i].Unquoted
		switch {
		case l.subcommand != "":
			switch {
			case i == 0 && t != l.subcommand:
				return ""
			case i > 0 && !isOption(args[i]):
				return t
			}
			continue
		case t == "-":
			return stdinScript(c)
		case strings.HasPrefix(t, "--"):
			name, value, attached := strings.Cut(t, "=")
			if slices.Contains(l.valuesLong, name) {
				if !attached {
					i++ // the value is the next word
				}
				continue
			}
			if !slices.Contains(l.evalLong, name) {
				continue
			}
			if !attached && i+1 < len(args) {
				value = args[i+1].Unquoted
			}
			return value
		case !isOption(args[i]):
			return "" // a script file
		}
	cluster:
		for j := 1; j < len(t); j++ {
			switch {
			case strings.IndexByte(l.eval, t[j]) >= 0:
				if j+1 < len(t) && !l.wordValues {
					return t[j+1:]
				}
				if i+1 < len(args) {
					if l.wordValues && isOption(args[i+1]) {
						break cluster // node -p -e '..': the -e that follows has the script
					}
					return args[i+1].Unquoted
				}
				return ""
			case strings.IndexByte(l.module, t[j]) >= 0:
				return ""
			case strings.IndexByte(l.values, t[j]) >= 0:
				if j+1 == len(t) {
					i++ // the value is the next word
				}
				break cluster
			}
		}
	}
	if l.subcommand != "" {
		return ""
	}
	return stdinScript(c)
}

// HereInput returns the here-document or here-string that is the command's standard input: the
// body of the document as written, or the string without its quotes.
func (c Command) HereInput() (text string, ok bool) {
	for k := len(c.Redirects) - 1; k >= 0; k-- {
		r := &c.Redirects[k]
		if r.Fd != "" && r.Fd != "0" {
			continue
		}
		switch r.Op {
		case "<<", "<<-":
			return r.Body, true
		case "<<<":
			return r.Target.Unquoted, true
		case "<", "<>", "<&":
			return "", false
		}
	}
	return "", false
}

func stdinScript(c *Command) string {
	text, _ := c.HereInput()
	return text
}

// scriptReads returns the files a script opens for reading by a literal path, and those of them
// it sends over the network: opened inside the arguments of a call that sends
// (requests.post(url, data=open(".env"))), or held by a name that is used there
// (d = open(".env").read(); requests.post(url, data=d)).
func scriptReads(l *language, script string) (files, sent []string) {
	var (
		open      callStack
		nameStart = -1 // where the last name starts and ends
		nameEnd   = -1
		nameSends bool // whether calling it sends
		afterSend = -1 // just after the ) of the last call that sends
		vars      scriptVars
		nest      int // the braces and brackets open
		quiet     quietArgs
	)
	// take records that the script has a file's content in hand at this point. A credential
	// (quiet) is neither sent nor in what holds it: in `r = requests.get(url, headers={'Authorization':
	// k})` r holds the response, and `headers = {'Authorization': k}` holds no file.
	take := func(path string) {
		if quiet.on {
			return
		}
		vars.statement = addPath(vars.statement, path)
		if vars.target != "" {
			vars.assigned = addPath(vars.assigned, path)
		}
		if open.sends != 0 {
			sent = addPath(sent, path)
		}
	}
	for i := 0; i < len(script); {
		c := script[i]
		switch {
		case c == '\'' || c == '"' || c == '`' && l.backtick:
			var text string
			text, i = stringLiteral(l, script, i)
			// A credential header's name as a key ('Authorization': ..), or as the start of the
			// header's line ('Authorization: Bearer ' . ..): its value is no data, wherever the
			// headers are built.
			if quotedCredentialHeader(text, script, i) {
				quiet = quietArgs{on: true, depth: open.depth, nest: nest}
			}
		case c == '#' && l.hash, c == '/' && l.slashes && strings.HasPrefix(script[i:], "//"):
			if end := strings.IndexByte(script[i:], '\n'); end >= 0 {
				i += end
			} else {
				i = len(script)
			}
		case c == '\n' || c == ';':
			if open.depth <= vars.depth {
				vars.assign()
			}
			if open.depth == 0 {
				vars.statement = nil
				quiet.on = false
			}
			// A credential written as a statement of a function's body (`const h = "Authorization:
			// " + k;`) ends with it, not with the body.
			if quiet.on && open.depth == quiet.depth && nest == quiet.nest && statementEnds(l, script, i) {
				quiet.on = false
			}
			i++
		case c == '(':
			open.push(nameSends && nameEnd >= 0 && skipSpaces(script, nameEnd) == i)
			if l.arrows {
				vars.groups = append(vars.groups, i)
			}
			i++
		case c == ')':
			if open.pop() {
				afterSend = i + 1
			}
			if n := len(vars.groups); n > 0 {
				vars.group, vars.groupEnd = vars.groups[n-1], i
				vars.groups = vars.groups[:n-1]
			}
			if open.depth < vars.depth {
				vars.assign()
			}
			if open.depth < quiet.depth {
				quiet.on = false
			}
			i++
		case c == '{' || c == '[':
			nest++
			i++
		case c == '}' || c == ']':
			nest--
			if nest < quiet.nest {
				quiet.on = false
			}
			i++
		case c == ',':
			if !quiet.rest && open.depth == quiet.depth && nest == quiet.nest {
				quiet.on = false
			}
			i++
		case c == '=' && l.arrows && strings.HasPrefix(script[i:], "=>"):
			// The parameters of a callback are given what the statement has in hand:
			// fs.readFile(".env", (err, data) => ..), readFile(".env").then(data => ..).
			before := i - 1
			for before >= 0 && (script[before] == ' ' || script[before] == '\t') {
				before--
			}
			switch {
			// A pair that closed just before the arrow; a `)` with no `(` before it closed none.
			case before >= 0 && script[before] == ')' && vars.groupEnd == before && vars.group < before:
				vars.bindAll(script[vars.group+1 : before])
			case before+1 == nameEnd:
				vars.bind(script[nameStart:nameEnd])
			}
			i += 2
		case c == '|' && l.blocks && opensBlock(script, i):
			// The parameters of a block: File.open(".env") { |f| .. }, .. do |f|.
			end := strings.IndexAny(script[i+1:], "|\n")
			if end < 0 || script[i+1+end] == '\n' {
				i++
				break
			}
			vars.bindAll(script[i+1 : i+1+end])
			i += end + 2
		case isNameByte(c) && (i == 0 || !isNameByte(script[i-1])):
			// A name with the names it is qualified by: fs.promises.readFile, Path, .read.
			end := i
			for end < len(script) && isNameByte(script[end]) {
				end++
			}
			name := script[i:end]
			nameStart, nameEnd = i, end
			nameSends = slices.ContainsFunc(l.sends, func(call string) bool { return isCall(name, call) }) ||
				afterSend >= 0 && name[0] == '.' && slices.Contains(l.chain, name[1:]) && strings.TrimSpace(script[afterSend:i]) == "" ||
				vars.sends(name, l.chain)
			if nameSends && vars.target != "" {
				vars.sender = true
			}
			i = end
			after := skipSpaces(script, end)
			switch path, opens := l.opens(script, nameStart, end); {
			case opens:
				files = append(files, path)
				take(path)
			case vars.binds:
				// with open(".env") as f
				vars.binds = false
				vars.bind(name)
			case l.as && name == "as":
				vars.binds = true
			case l.arrows && name == "function":
				// function (err, data) { .. }, as a callback
				if params, length, ok := parameters(script[end:]); ok {
					vars.bindAll(params)
					i = end + length
				}
			case open.depth > 0 && slices.Contains(l.quiet, name) && quietName(l, script, name, after):
				// A call's credentials: not data, up to the next argument (auth=.. where name=value
				// in a call is an argument, auth: ..), or to the end of the call for an option that
				// says what the argument after it is (CURLOPT_USERPWD, ..). A shorthand property
				// (auth,) is a name and holds no file this script opened by its own text, and a
				// JavaScript `const auth = ..` is a variable like any other.
				quiet = quietArgs{on: true, rest: strings.HasPrefix(name, "CURLOPT_"), depth: open.depth, nest: nest}
			case isAssignment(script, after):
				// name = value. Inside a call's parentheses python means an argument by it.
				if name[0] != '.' && (!l.keywords || open.depth == 0) {
					vars.assign()
					vars.target, vars.depth = name, open.depth
				}
			case after < len(script) && script[after] == ':' && !strings.HasPrefix(script[after:], "::"):
				// A key (body: data) names what is sent; it is not the value. A credential header
				// written as a key (Authorization: ..) holds no data.
				if isCredentialHeader(name) {
					quiet = quietArgs{on: true, depth: open.depth, nest: nest}
				}
			default:
				for _, path := range vars.held(name) {
					take(path)
				}
			}
		default:
			i++
		}
	}
	return files, sent
}

// quietArgs is the part of a script in which nothing is data, a credential: from one of the
// language's quiet names or a credential header's name to the next comma at the same depth of
// parentheses and of braces and brackets, or the end of its statement, or, with rest, to the end
// of the call.
type quietArgs struct {
	on, rest    bool
	depth, nest int
}

// quietName reports whether one of the language's quiet names, ending at after's blanks, names
// a credential argument there: auth=.. where name=value in a call is an argument (python),
// auth: .. as a key, and a CURLOPT_ option followed by the argument it says the meaning of.
func quietName(l *language, script, name string, after int) bool {
	if strings.HasPrefix(name, "CURLOPT_") {
		return after < len(script) && script[after] == ','
	}
	return l.keywords && isAssignment(script, after) ||
		after < len(script) && script[after] == ':' && !strings.HasPrefix(script[after:], "::")
}

// statementEnds reports whether the ; or newline at script[i] ends a statement: a ; always, and a
// newline where the language ends a statement at one (javascript, ruby; python and php not
// inside brackets) and the line does not go on with an operator or a method on the next one.
func statementEnds(l *language, script string, i int) bool {
	if script[i] == ';' {
		return true
	}
	if !l.arrows && !l.blocks {
		return false
	}
	before := strings.TrimRight(script[:i], " \t\r")
	if before == "" || strings.IndexByte("+-*/%&|,:([{=?.<>!", before[len(before)-1]) >= 0 {
		return false
	}
	next := skipSpaces(script, i+1)
	return next >= len(script) || script[next] != '.' && script[next] != '?'
}

// quotedCredentialHeader reports whether a string literal that ended at script[end] names a
// header that holds a credential: as a key ('Authorization': ..), or at the start of the header's
// line ('Authorization: Bearer ' . $token).
func quotedCredentialHeader(text, script string, end int) bool {
	if name, _, ok := strings.Cut(text, ":"); ok {
		return isCredentialHeader(strings.TrimSpace(name))
	}
	after := skipSpaces(script, end)
	return after < len(script) && script[after] == ':' && isCredentialHeader(text)
}

// isCredentialHeader reports whether an HTTP header's name says it holds a credential:
// Authorization, Proxy-Authorization, Cookie, and a prefixed API key or token header
// (X-Api-Key, X-Auth-Token). Header names join their words with -: a bare token, or a field
// written with _ (access_token, my_api_key), is as often a field of the data sent, and is data.
func isCredentialHeader(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "authorization", "proxy-authorization", "cookie":
		return true
	}
	return strings.HasSuffix(n, "-api-key") || strings.HasSuffix(n, "-apikey") || strings.HasSuffix(n, "-token")
}

// scriptVars are the names a script gives the content of the files it opens, as far as its text
// shows without running it: an assignment whose value opens a file or uses a name that holds
// one (d = open(".env").read(), body = d.encode()), and a name bound to what a statement opened
// (python's `with open(..) as f`, the parameters of a javascript callback, of a ruby block). A
// name holds the files until it is assigned again. A name assigned what a call that sends
// returns (r = https.request(url)) sends by the methods that do on such a value (r.write(..)).
// Nothing is known of scopes, of what a function returns, or of a value a string is built
// around (an f-string, a template literal).
type scriptVars struct {
	holds   map[string][]string
	senders map[string]bool
	// target is the name the statement at hand assigns, depth the parentheses open where it
	// does, assigned the files its value has so far, and sender whether a call that sends is
	// in it.
	target   string
	depth    int
	assigned []string
	sender   bool
	// statement are the files the statement at hand opened or used a name of. binds is set
	// when the next name is bound to them.
	statement []string
	binds     bool
	// groups are the parentheses open, by where they open, and group and groupEnd the last pair
	// that closed: the parameter list, when => follows it.
	groups          []int
	group, groupEnd int
}

// assign ends the assignment at hand: its name holds what its value had, or nothing.
func (v *scriptVars) assign() {
	if v.target == "" {
		return
	}
	if len(v.assigned) == 0 {
		delete(v.holds, v.target)
	} else {
		v.hold(v.target, v.assigned)
	}
	if v.sender {
		if v.senders == nil {
			v.senders = map[string]bool{}
		}
		v.senders[v.target] = true
	} else {
		delete(v.senders, v.target)
	}
	v.target, v.assigned, v.sender = "", nil, false
}

// sends reports whether name is one of the chain methods of a name that was assigned what a
// call that sends returns.
func (v *scriptVars) sends(name string, chain []string) bool {
	dot := strings.LastIndexByte(name, '.')
	return dot > 0 && v.senders[name[:dot]] && slices.Contains(chain, name[dot+1:])
}

// bind gives a name the files the statement at hand has.
func (v *scriptVars) bind(name string) {
	if len(v.statement) > 0 && name != "" && name[0] != '.' {
		v.hold(name, slices.Clone(v.statement))
	}
}

// bindAll binds each name in a parameter list.
func (v *scriptVars) bindAll(params string) {
	if len(v.statement) == 0 {
		return
	}
	for i := 0; i < len(params); {
		if !isNameByte(params[i]) {
			i++
			continue
		}
		end := i
		for end < len(params) && isNameByte(params[end]) {
			end++
		}
		v.bind(params[i:end])
		i = end
	}
}

func (v *scriptVars) hold(name string, files []string) {
	if v.holds == nil {
		v.holds = map[string][]string{}
	}
	v.holds[name] = files
}

// held returns the files a name holds: the name itself, or what it is a member of (f.read of f,
// self.body.encode of self.body).
func (v *scriptVars) held(name string) []string {
	for v.holds != nil {
		if files := v.holds[name]; files != nil {
			return files
		}
		dot := strings.LastIndexByte(name, '.')
		if dot <= 0 {
			break
		}
		name = name[:dot]
	}
	return nil
}

// isAssignment reports whether the = of an assignment is at i: not ==, => or =~.
func isAssignment(script string, i int) bool {
	if i >= len(script) || script[i] != '=' {
		return false
	}
	return i+1 >= len(script) || !strings.ContainsRune("=>~", rune(script[i+1]))
}

// opensBlock reports whether the | at i follows the { or the do that opens a ruby block.
func opensBlock(script string, i int) bool {
	i--
	for i >= 0 && (script[i] == ' ' || script[i] == '\t') {
		i--
	}
	if i < 0 {
		return false
	}
	return script[i] == '{' || strings.HasSuffix(script[:i+1], "do") && (i < 2 || !isNameByte(script[i-2]))
}

// parameters returns the parameter list that follows the function keyword, after its name if it
// has one, and the length of the text up to the list's closing parenthesis.
func parameters(after string) (params string, length int, ok bool) {
	open := strings.IndexByte(after, '(')
	if open < 0 || strings.ContainsFunc(strings.TrimSpace(after[:open]), func(r rune) bool { return r > 0x7f || !isNameByte(byte(r)) }) {
		return "", 0, false
	}
	end := strings.IndexByte(after[open:], ')')
	if end < 0 {
		return "", 0, false
	}
	return after[open+1 : open+end], open + end + 1, true
}

func addPath(paths []string, path string) []string {
	if slices.Contains(paths, path) {
		return paths
	}
	return append(paths, path)
}

// callStack is the parentheses open at a point of a script, and which of them are the arguments
// of a call that sends. Past 64 deep it only counts.
type callStack struct {
	depth int
	sends uint64
}

func (s *callStack) push(sends bool) {
	if sends && s.depth < 64 {
		s.sends |= 1 << s.depth
	}
	s.depth++
}

// pop closes the innermost parenthesis and reports whether it was a call that sends.
func (s *callStack) pop() bool {
	if s.depth == 0 {
		return false
	}
	s.depth--
	if s.depth >= 64 {
		return false
	}
	bit := uint64(1) << s.depth
	sends := s.sends&bit != 0
	s.sends &^= bit
	return sends
}

// isCall reports whether name, as written before a parenthesis, is call. A call without a dot in
// it is also matched as a method (io.open, fs.promises.readFile).
func isCall(name, call string) bool {
	return name == call || !strings.Contains(call, ".") && strings.HasSuffix(name, "."+call)
}

// opens reports whether the name at script[i:end] is one of the language's calls, with a literal
// path as its first argument that it reads, and returns the path.
func (l *language) opens(script string, i, end int) (path string, ok bool) {
	name := script[i:end]
	for _, call := range l.calls {
		if !isCall(name, call) {
			continue
		}
		j := skipSpaces(script, end)
		bare := j >= len(script) || script[j] != '('
		if !bare {
			j = skipSpaces(script, j+1)
		} else if !l.bare || j == end {
			return "", false
		}
		path, j, ok = l.literal(script, j)
		if !ok || path == "" || strings.Contains(path, "://") || strings.ContainsAny(path, "\n{") {
			return "", false
		}
		if l.modes && l.writes(script, j, bare) {
			return "", false
		}
		if follow, needed := l.then[call]; needed {
			after := skipSpaces(script, j)
			if after >= len(script) || script[after] != ')' {
				return "", false
			}
			after = skipSpaces(script, after+1)
			if !slices.ContainsFunc(follow, func(method string) bool { return strings.HasPrefix(script[after:], method) }) {
				return "", false
			}
		}
		return path, true
	}
	return "", false
}

// literal reads a string literal at i, after any prefix letters, and returns its text as written
// between the quotes and where it ends.
func (l *language) literal(script string, i int) (text string, end int, ok bool) {
	for i < len(script) && strings.IndexByte(l.prefixes, script[i]) >= 0 {
		i++
	}
	if i >= len(script) || script[i] != '\'' && script[i] != '"' && !(script[i] == '`' && l.backtick) {
		return "", 0, false
	}
	text, end = stringLiteral(l, script, i)
	return text, end, true
}

// writes reports whether the arguments after a path, from i to the end of the call, open the file
// to write: a second string argument or a mode=".." argument anywhere, with w, a or x in it
// (open(p, encoding='utf8', mode='w')), or the O_WRONLY flag of os.open. A bare call, written
// without parentheses, ends with its line or statement.
func (l *language) writes(script string, i int, bare bool) bool {
	arg := 0 // the path is argument 0
	for depth := 0; i < len(script); {
		switch c := script[i]; {
		case c == '(' || c == '[' || c == '{':
			depth++
			i++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return false
			}
			depth--
			i++
		case bare && depth == 0 && (c == '\n' || c == ';'):
			return false
		case c == '\'' || c == '"' || c == '`' && l.backtick:
			_, i = stringLiteral(l, script, i)
		case depth == 0 && c == ',':
			arg++
			i = skipSpaces(script, i+1)
			if strings.HasPrefix(script[i:], "mode") {
				if j := skipSpaces(script, i+len("mode")); j < len(script) && script[j] == '=' {
					mode, _, ok := l.literal(script, skipSpaces(script, j+1))
					return ok && strings.ContainsAny(mode, "wax")
				}
			}
			if mode, _, ok := l.literal(script, i); ok && arg == 1 {
				return strings.ContainsAny(mode, "wax")
			}
		case strings.HasPrefix(script[i:], "O_WRONLY"):
			return true
		default:
			i++
		}
	}
	return false
}

// stringLiteral returns the text of the string that opens at i and the index after its closing
// quote, or the end of the script when it is not closed.
func stringLiteral(l *language, script string, i int) (text string, end int) {
	quote := script[i : i+1]
	if l.triple && strings.HasPrefix(script[i:], quote+quote+quote) {
		quote = script[i : i+3]
	}
	start := i + len(quote)
	for j := start; j < len(script); j++ {
		switch {
		case script[j] == '\\':
			j++
		case strings.HasPrefix(script[j:], quote):
			return script[start:j], j + len(quote)
		}
	}
	return script[start:], len(script)
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// isNameByte reports whether c can be part of a call's name: a letter, a digit, _ or the dot of a
// qualified one.
func isNameByte(c byte) bool {
	return c == '_' || c == '.' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}
