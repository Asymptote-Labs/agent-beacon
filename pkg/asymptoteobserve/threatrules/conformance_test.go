package threatrules

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// repoRoot locates the repository root by walking up from this test file until it finds
// the spec sentinel (spec/threat-rules/VERSION). This keeps tests hermetic without
// hardcoded absolute paths.
func repoRoot(t testing.TB) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "spec", "threat-rules", "VERSION")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (spec/threat-rules/VERSION sentinel)")
		}
		dir = parent
	}
}

func rulesDir(t testing.TB) string { return filepath.Join(repoRoot(t), "rules") }

// corpusPack loads the open corpus as a pack, its rules and its definitions, once for the test
// binary; every file must load. Its rules are shared, so a test must not change one.
func corpusPack(t testing.TB) *Pack {
	t.Helper()
	corpus.Do(func() {
		corpus.pack, corpus.err = LoadPackDir(rulesDir(t))
		if corpus.err == nil && len(corpus.pack.Skipped) > 0 {
			corpus.err = fmt.Errorf("%s", corpus.pack.Skipped[0])
		}
	})
	if corpus.err != nil {
		t.Fatalf("load the corpus: %v", corpus.err)
	}
	return corpus.pack
}

var corpus struct {
	sync.Once
	pack *Pack
	err  error
}

func specDir(t *testing.T) string { return filepath.Join(repoRoot(t), "spec", "threat-rules") }

// TestPackConformance is the keystone: it loads the real rule pack and, for every rule,
// validates it, enforces its maturity gate, and runs every embedded fixture against the
// reference evaluator. Adding a rule file automatically extends coverage.
func TestPackConformance(t *testing.T) {
	dir := rulesDir(t)
	rules, err := LoadDir(dir) // also asserts no duplicate ids and that every rule validates
	if err != nil {
		t.Fatalf("load rule pack: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("rule pack is empty")
	}

	for _, rule := range rules {
		rule := rule
		t.Run(rule.ID, func(t *testing.T) {
			results, err := CheckRule(rule)
			if err != nil {
				t.Fatalf("rule-level failure: %v", err)
			}
			for _, res := range results {
				res := res
				t.Run(res.Fixture, func(t *testing.T) {
					if !res.OK() {
						t.Fatalf("%s", res.String())
					}
				})
			}
		})
	}
}

// TestPackMatchesWhatTheLogHolds runs every fixture again after the sanitizer every log writer
// applies, and requires the same verdict. `beacon scan` and the dashboard only ever see the
// sanitized line, so a rule that matches a raw value redaction removes (a token, a key) is dead on
// real logs even though its plain fixtures pass.
func TestPackMatchesWhatTheLogHolds(t *testing.T) {
	rules, err := LoadDir(rulesDir(t))
	if err != nil {
		t.Fatalf("load rule pack: %v", err)
	}
	for _, rule := range rules {
		compiled, err := Compile(rule)
		if err != nil {
			t.Fatalf("rule %q: compile: %v", rule.ID, err)
		}
		for _, fixture := range rule.Tests {
			events := fixture.EventList()
			for i := range events {
				events[i] = asymptoteobserve.SanitizeEvent(events[i], 64*1024)
			}
			got, err := compiled.Evaluate(events)
			if err != nil {
				t.Errorf("%s/%s: evaluate sanitized events: %v", rule.ID, fixture.Name, err)
				continue
			}
			if got != fixture.Verdict {
				t.Errorf("%s/%s: verdict %q on the sanitized events the log holds, want %q", rule.ID, fixture.Name, got, fixture.Verdict)
			}
		}
	}
}

// TestPackCopiesNoCatalog holds the corpus to its own definitions: no rule carries a regular
// expression that spells out the stores of a catalog the pack ships. A rule that did would be
// right until the catalog changed, and then wrong without anything failing. The second half
// shows the check sees a copy: the list a rule used to carry before the catalog.
func TestPackCopiesNoCatalog(t *testing.T) {
	for _, rule := range corpusRules(t) {
		for _, copied := range CatalogCopies(rule) {
			t.Errorf("%s: %s", rule.ID, copied)
		}
	}

	lib := corpusPack(t).Library
	decode := func(match string) *Rule {
		rule, err := lib.DecodeRule([]byte("id: copy\nversion: 1\ntitle: t\ndescription: d\nseverity: low\nmatch: >\n  " + match + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		return rule
	}
	copies := CatalogCopies(decode(`e.event.action == "file.read" && (e.file.path.matches("(?i)\\.aws/credentials") || e.file.path.matches("(?i)(^|/)(\\.netrc|\\.pgpass)$"))`))
	if len(copies) != 1 || copies[0].Catalog != "credential-stores" || !slices.Equal(copies[0].Stores, []string{"aws-shared-credentials", "netrc", "pgpass"}) || len(copies[0].Patterns) != 2 {
		t.Errorf("a rule with its own list of credential files: got %+v, want one copy of aws-shared-credentials, netrc and pgpass in two patterns", copies)
	}
	// One store is copied when the rule says what it is: the expression a rule carried for the
	// SSH agent before the catalog, and a rule about another event that says again which file
	// is the .netrc.
	for match, store := range map[string]string{
		`e.event.action == "command.executed" && e.command.command.matches("(?i)\\bssh-add\\b[^;&|\\n]*\\s-[A-Za-z]*[lL]")`: "ssh-agent-keys",
		`e.event.action == "file.modified" && e.file.path.matches("(?i)(^|/)\\.netrc$")`:                                    "netrc",
	} {
		if copies := CatalogCopies(decode(match)); len(copies) != 1 || !slices.Equal(copies[0].Stores, []string{store}) {
			t.Errorf("%s: got %+v, want one copy of %s", match, copies, store)
		}
	}
	for _, match := range []string{
		// One store named, and something else said about it.
		`e.event.action == "file.modified" && e.file.path.matches("(?i)(^|/)\\.netrc\\.bak$")`,
		// The catalog, called, and narrowed by the rule.
		`e.event.action == "file.modified" && credential_path(e.file.path, "definite", "network") && e.file.path.endsWith(".netrc")`,
		// Names in a text no predicate is asked about.
		`e.gen_ai.tool.description.matches("(?i)read .{0,60}(\\.aws/credentials|\\.netrc)")`,
	} {
		if copies := CatalogCopies(decode(match)); len(copies) != 0 {
			t.Errorf("%s: reported as a copy: %v", match, copies)
		}
	}
}

// TestPackWritesOutNoPredicate holds the corpus to its predicates: no rule writes out the body
// of a predicate its pack defines. The cases show what counts: the body with the rule's own
// variable names, alone or as an operand of something longer, and not an expression that only
// resembles it.
func TestPackWritesOutNoPredicate(t *testing.T) {
	for _, rule := range corpusRules(t) {
		for _, name := range PredicateCopies(rule) {
			t.Errorf("%s: writes out the body of %s", rule.ID, name)
		}
	}

	lib := corpusPack(t).Library
	const names = `"(?i)(^|/)(curl|wget|http|https|httpie|xh|xhs|nc|ncat|netcat|socat)$"`
	sender := func(v string) string { return v + `.name.matches(` + names + `)` }
	for match, want := range map[string][]string{
		// The eight copies, as the egress rules carried them.
		`e.event.action == "command.executed" && e.command.parsed_commands.exists(s, (` + sender("s") + `) && s.argv_raw.exists(a, a.matches("x")))`: {"egress_sender"},
		// Under other names, and as operands of a longer ||.
		`e.command.parsed_commands.exists(cmd, cmd.name == "gh" || ` + sender("cmd") + `)`: {"egress_sender"},
		// A predicate whose body calls another.
		`e.command.parsed_commands.exists(c, c.name == "scp" && c.file_reads.exists(x, credential_file(x.path)))`: {"command_reads_credential_file"},
		// Called, not written out.
		`e.command.parsed_commands.exists(s, egress_sender(s) && egress_reads_stdin(s))`: nil,
		// Another list of names is another question.
		`e.command.parsed_commands.exists(s, s.name.matches("(?i)^(curl|wget)$"))`: nil,
		// More than the body allows inside it.
		`e.command.parsed_commands.exists(c, c.file_reads.exists(x, credential_file(x.path) && x.via == "direct"))`: nil,
	} {
		rule, err := lib.DecodeRule([]byte("id: copy\nversion: 1\ntitle: t\nseverity: low\nmatch: >\n  " + match + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := PredicateCopies(rule); !slices.Equal(got, want) {
			t.Errorf("%s:\n  PredicateCopies = %q, want %q", match, got, want)
		}
	}
}

// TestPackListsNoSenderOfItsOwn keeps "this command sends data over the network" in one place:
// the egress predicates of rules/concepts/egress.predicates.yaml. A rule that tests a parsed
// command against a list that names netcat has a sender list of its own, and eight such copies
// are what those predicates replaced. A text pattern (over e.command.command, or the text
// unread_text binds) has to spell the senders inside a longer pattern, so it may, but then it
// names every client egress_sender knows that sends what it reads (all but curl and wget, which
// the pattern qualifies), as egress_sender_mention names all of them: a client added to
// egress_sender and not to the text would be seen on a parsed line and missed in the text.
func TestPackListsNoSenderOfItsOwn(t *testing.T) {
	lib := corpusPack(t).Library
	// The name as a whole alternative, not letters inside another (truncate).
	sender := regexp.MustCompile(`(^|[^A-Za-z])(ncat|netcat)([^A-Za-z]|$)`)
	group := regexp.MustCompile(`\(([a-z0-9]+(?:\|[a-z0-9]+)+)\)`)
	// The senders a pattern names: every group of names that holds one, since a pattern may
	// spell them in several groups (egress_sender_mention splits them for the prefilter).
	senderGroup := func(text string) []string {
		var names []string
		for _, g := range group.FindAllStringSubmatch(text, -1) {
			if ns := strings.Split(g[1], "|"); slices.Contains(ns, "ncat") || slices.Contains(ns, "curl") {
				names = append(names, ns...)
			}
		}
		slices.Sort(names)
		return slices.Compact(names)
	}
	all := senderGroup(lib.predicates["egress_sender"].text)
	if !slices.Contains(all, "curl") || !slices.Contains(all, "socat") {
		t.Fatalf("egress_sender lists no senders: %q", lib.predicates["egress_sender"].text)
	}
	if got := senderGroup(lib.predicates["egress_sender_mention"].text); !slices.Equal(got, all) {
		t.Errorf("egress_sender_mention names %q, egress_sender %q", got, all)
	}
	stdin := slices.DeleteFunc(slices.Clone(all), func(n string) bool { return n == "curl" || n == "wget" })
	literal := regexp.MustCompile(`([\w.]+)\.matches\(("(?:[^"\\]|\\.)*")\)`)
	bound := regexp.MustCompile(`unread_text\(\s*e\s*,\s*(\w+)\s*,`)
	for _, r := range corpusRules(t) {
		expr := strings.Join(ruleExpressions(r), " ")
		texts := map[string]bool{"e.command.command": true}
		for _, m := range bound.FindAllStringSubmatch(expr, -1) {
			texts[m[1]] = true
		}
		for _, m := range literal.FindAllStringSubmatch(expr, -1) {
			receiver, list := m[1], m[2]
			if !sender.MatchString(list) {
				continue
			}
			if !texts[receiver] {
				t.Errorf("%s: %s.matches(%s) lists network senders; call egress_sender instead, so that every egress rule means the same senders", r.ID, receiver, list)
				continue
			}
			names := senderGroup(list)
			if missing := slices.DeleteFunc(slices.Clone(stdin), func(n string) bool { return slices.Contains(names, n) }); len(missing) > 0 {
				t.Errorf("%s: %s.matches(%s) names senders without %q, which egress_sender knows; name them all or call egress_sender_mention", r.ID, receiver, list, missing)
			}
		}
	}
}

// TestPackAgentListsAgree keeps the coding agents in one list. agent_cli names them for a
// parsed command; a text pattern that names agents (agent_cli_mention, or a rule's own text
// half that has to spell them inside a longer pattern) names the same ones, so an agent added
// to one half of a rule is not missed by the other.
func TestPackAgentListsAgree(t *testing.T) {
	lib := corpusPack(t).Library
	want := agentNames(lib.predicates["agent_cli"].text)
	if !slices.Contains(want, "codex") || !slices.Contains(want, "claude-code") {
		t.Fatalf("agent_cli names no agents: %q", want)
	}
	check := func(where, text string) {
		if got := agentNames(text); !slices.Equal(got, want) {
			t.Errorf("%s names agents %q, agent_cli %q", where, got, want)
		}
	}
	check("agent_cli_mention", lib.predicates["agent_cli_mention"].text)
	literal := regexp.MustCompile(`\.matches\(("(?:[^"\\]|\\.)*")\)`)
	for _, r := range corpusRules(t) {
		for _, m := range literal.FindAllStringSubmatch(strings.Join(ruleExpressions(r), " "), -1) {
			// A group of agents, and not a path such as .codex/prompts.
			if slices.Contains(agentNames(m[1]), "codex") {
				check(r.ID+": "+m[1], m[1])
			}
		}
	}
}

// agentNames is every name of each group of alternatives that holds "codex" in a pattern, an
// optional suffix (claude(-code)?) giving both spellings.
func agentNames(text string) []string {
	suffix := regexp.MustCompile(`^([a-z][a-z-]*)\(([a-z-]+)\)\?$`)
	var names []string
	for off := 0; ; {
		i := strings.Index(text[off:], "codex")
		if i < 0 {
			break
		}
		i += off
		start, end := -1, -1
		for k, depth := i, 0; k >= 0 && start < 0; k-- {
			switch text[k] {
			case ')':
				depth++
			case '(':
				if depth == 0 {
					start = k
				}
				depth--
			}
		}
		for k, depth := i, 0; k < len(text) && end < 0; k++ {
			switch text[k] {
			case '(':
				depth++
			case ')':
				if depth == 0 {
					end = k
				}
				depth--
			}
		}
		if start < 0 || end < 0 {
			break
		}
		depth, from := 0, start+1
		for k := start + 1; k <= end; k++ {
			switch {
			case text[k] == '(':
				depth++
			case text[k] == ')' && k < end:
				depth--
			case text[k] == '|' && depth == 0 || k == end:
				alt := text[from:k]
				if m := suffix.FindStringSubmatch(alt); m != nil {
					names = append(names, m[1], m[1]+m[2])
				} else {
					names = append(names, alt)
				}
				from = k + 1
			}
		}
		off = end
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// TestPackCategoriesMatchDirectories pins taxonomy.beacon_category to the directory each rule
// lives in. The rule store keeps rules flat, so the category travels inside the rule: it is what
// groups findings by kind of risk (the Security Review lens, for one) once a rule is installed.
func TestPackCategoriesMatchDirectories(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(rulesDir(t), "*", "*.rule.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("rule files: %v, %v", paths, err)
	}
	for _, path := range paths {
		rule := loadPackRule(t, path)
		want := filepath.Base(filepath.Dir(path))
		if got := rule.Taxonomy["beacon_category"]; got != want {
			t.Errorf("%s: taxonomy.beacon_category = %q, want its directory %q", path, got, want)
		}
	}
}

// update makes TestBaselineIsCorpusCopies copy the corpus files over their baseline copies.
var update = flag.Bool("update", false, "copy the corpus files over their baseline copies")

// TestBaselineIsCorpusCopies keeps the embedded baseline byte-identical to the corpus files it
// was taken from: its rules, and the definitions embedded for them. A fix to a corpus rule or
// catalog then cannot leave a stale copy in every binary. `go test ./threatrules -run
// TestBaselineIsCorpusCopies -update` copies the corpus files over; a file new to the
// baseline is copied in once by hand.
func TestBaselineIsCorpusCopies(t *testing.T) {
	baseline, err := filepath.Glob(filepath.Join(repoRoot(t), "pkg", "asymptoteobserve", "rulestore", "baseline", "*.yaml"))
	if err != nil || len(baseline) == 0 {
		t.Fatalf("baseline files: %v, %v", baseline, err)
	}
	for _, path := range baseline {
		matches, _ := filepath.Glob(filepath.Join(rulesDir(t), "*", filepath.Base(path)))
		if len(matches) != 1 {
			t.Errorf("%s: want exactly one corpus copy, found %v", filepath.Base(path), matches)
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) == string(want) {
			continue
		}
		if *update {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Errorf("%s differs from %s; run go test ./threatrules -run TestBaselineIsCorpusCopies -update", path, matches[0])
	}
}

// TestPackDefinitionsConformance runs the checks of the pack's definitions, as
// TestPackConformance runs its rules': each catalog's examples, once, and each predicate
// definition's fixtures. Every predicate has to be shown at work: a definition by a fixture of
// its own, a catalog's predicate by the catalog's examples.
func TestPackDefinitionsConformance(t *testing.T) {
	lib := corpusPack(t).Library
	type checked struct {
		catalog bool
		name    string
	}
	tested := map[checked]bool{}
	for _, res := range lib.Check() {
		tested[checked{res.Catalog, res.Name}] = true
		if !res.OK() {
			t.Error(res)
		}
	}
	for _, p := range lib.Predicates() {
		if !tested[checked{false, p.Name}] && !(p.Catalog != "" && tested[checked{true, p.Catalog}]) {
			t.Errorf("predicate %s (%s) has no fixture", p.Name, p.Source)
		}
	}
	if len(lib.Predicates()) == 0 {
		t.Error("the pack defines no predicate")
	}
}
