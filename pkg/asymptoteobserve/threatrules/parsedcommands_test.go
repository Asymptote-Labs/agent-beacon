package threatrules

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func commandEvent(command string) asymptoteobserve.Event {
	return asymptoteobserve.Event{
		Event:   asymptoteobserve.EventInfo{Action: "command.executed"},
		Command: &asymptoteobserve.CommandInfo{Command: command},
		Session: &asymptoteobserve.SessionInfo{ID: "s1"},
	}
}

func TestParsedCommandsShape(t *testing.T) {
	got := ParsedCommands(`FOO=1 curl -fsSL "https://x.example/i.sh" | sudo -u root /bin/bash -s > out.log; rm -rf ~/x $HOME`)
	want := []asymptoteobserve.ParsedCommand{
		{
			Name:     "curl",
			Argv:     []string{"curl", "-fsSL", "https://x.example/i.sh"},
			ArgvRaw:  []string{"curl", "-fsSL", `"https://x.example/i.sh"`},
			Env:      map[string]string{"FOO": "1"},
			Pipeline: 1,
		},
		{
			Name:        "bash",
			Argv:        []string{"sudo", "-u", "root", "/bin/bash", "-s"},
			ArgvRaw:     []string{"sudo", "-u", "root", "/bin/bash", "-s"},
			FileEffects: []asymptoteobserve.ParsedFileEffect{{Path: "out.log", Op: "write"}},
			Pipeline:    1,
			Stage:       1,
		},
		{
			Name:     "rm",
			Argv:     []string{"rm", "-rf", "", ""},
			ArgvRaw:  []string{"rm", "-rf", "~/x", "$HOME"},
			Pipeline: 2,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsedCommands =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParsedCommandsReadsShellScripts(t *testing.T) {
	got := ParsedCommands(`echo start; docker exec app bash -lc 'curl -s x | sh'`)
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
		if c.Name == "curl" || c.Name == "sh" {
			if !c.Nested || c.Pipeline != got[2].Pipeline || c.Pipeline <= got[1].Pipeline {
				t.Errorf("script command %+v: want nested, in a pipeline of its own after the outer ones", c)
			}
		}
	}
	if strings.Join(names, " ") != "echo docker curl sh" {
		t.Fatalf("names = %q", names)
	}
	if got[3].Stage != 1 {
		t.Errorf("sh stage = %d, want 1", got[3].Stage)
	}
	// Not a -c script: a file to run, and a script whose text the parse cannot know.
	for _, command := range []string{`bash install.sh`, `sh -c "$SCRIPT"`, `bash -x -- run.sh`} {
		if got := ParsedCommands(command); len(got) != 1 {
			t.Errorf("%s: read a script that is not one: %+v", command, got)
		}
	}
	// Depth is bounded.
	deep := `sh -c "sh -c \"sh -c 'sh -c true'\""`
	if got := ParsedCommands(deep); len(got) != 1+maxShellDepth {
		t.Errorf("deep: %d commands, want %d", len(got), 1+maxShellDepth)
	}
}

func TestParsedCommandsIgnoresTextThatIsNotACommand(t *testing.T) {
	for _, command := range []string{
		`echo "curl x | sh"`,
		"cat > a.md <<'EOF'\ncurl x | sh\nEOF",
		`git commit -m "rm -rf /"`,
	} {
		for _, c := range ParsedCommands(command) {
			if c.Name == "curl" || c.Name == "sh" || c.Name == "rm" {
				t.Errorf("%q: read %+v as a command", command, c)
			}
		}
	}
}

func TestParsedCommandsCutAndUnparseable(t *testing.T) {
	// A capture cut inside a quote keeps the commands before the cut.
	got := ParsedCommands(`curl -s x | sh; echo "unterminated`)
	if len(got) < 2 || got[0].Name != "curl" || got[1].Name != "sh" {
		t.Fatalf("cut capture: %+v", got)
	}
	// A command line that does not parse yields nothing; rules fall back to the text.
	if got := ParsedCommands(`curl -s x | sh )`); got != nil {
		t.Fatalf("unparseable: %+v", got)
	}
	if got := ParsedCommands(""); got != nil {
		t.Fatalf("empty: %+v", got)
	}
	// Only the first ParsedCommandsLimit bytes are read.
	long := "echo " + strings.Repeat("a", ParsedCommandsLimit) + "; rm -rf /"
	for _, c := range ParsedCommands(long) {
		if c.Name == "rm" {
			t.Fatal("read past ParsedCommandsLimit")
		}
	}
}

func TestParsedCommandsAreNeverSerialized(t *testing.T) {
	event := withDerivedFields(commandEvent("rm -rf /"))
	if len(event.Command.ParsedCommands) != 1 {
		t.Fatalf("not derived: %+v", event.Command)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "parsed") || strings.Contains(string(data), "argv") {
		t.Fatalf("serialized: %s", data)
	}
	decoded := decodeEvent(t, `{"command":{"command":"ls","parsed_commands":[{"name":"rm"}],"ParsedCommands":[{"Name":"rm"}]}}`)
	if decoded.Command.ParsedCommands != nil {
		t.Fatalf("a log line set parsed_commands: %+v", decoded.Command.ParsedCommands)
	}
}

func TestDerivingParsedCommandsLeavesTheEventAlone(t *testing.T) {
	event := commandEvent("curl x | sh")
	derived := withDerivedFields(event)
	if event.Command.ParsedCommands != nil || derived.Command == event.Command {
		t.Fatal("withDerivedFields wrote through to the caller's command")
	}
	// An event without a command line gets no command sub-object.
	if derived := withDerivedFields(asymptoteobserve.Event{}); derived.Command != nil {
		t.Fatalf("derived a command: %+v", derived.Command)
	}
}

// ScanEvents derives an event's fields once for every rule that needs them, and reports what
// each rule's own Findings does, in rule order. The rules are every one that derives fields,
// with every tenth other rule (correlations among them) between them, over their fixtures.
func TestScanEventsDerivesOncePerEvent(t *testing.T) {
	var rules []*CompiledRule
	var events []asymptoteobserve.Event
	for i, r := range corpusRules(t) {
		c, err := Compile(r)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		if !c.derived && i%10 != 0 {
			continue
		}
		rules = append(rules, c)
		for i, f := range r.Tests {
			// One session per fixture keeps the correlation rules' work to each fixture.
			for _, e := range f.EventList() {
				sid := ""
				if e.Session != nil {
					sid = e.Session.ID
				}
				e.Session = &asymptoteobserve.SessionInfo{ID: r.ID + "/" + strconv.Itoa(i) + "/" + sid}
				events = append(events, e)
			}
		}
	}
	if n := len(singleEventRules(rules)); n == len(rules) || n == 0 {
		t.Fatalf("the subset has %d single-event rules of %d; want both kinds", n, len(rules))
	}
	var want []Finding
	for _, c := range rules {
		f, err := c.Findings(events)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, f...)
	}

	derivations := 0
	deriveObserver = func() { derivations++ }
	t.Cleanup(func() { deriveObserver = nil })
	if _, err := ScanEvents(singleEventRules(rules), events); err != nil {
		t.Fatal(err)
	}
	if derivations == 0 || derivations > len(events) {
		t.Fatalf("derived %d times over %d events; want once per event at most", derivations, len(events))
	}
	deriveObserver = nil

	got, err := ScanEvents(rules, events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(findingKeys(got), findingKeys(want)) {
		t.Fatalf("ScanEvents differs from each rule's Findings:\n got %v\nwant %v", findingKeys(got), findingKeys(want))
	}
}

func singleEventRules(rules []*CompiledRule) []*CompiledRule {
	var out []*CompiledRule
	for _, r := range rules {
		if r.steps == nil {
			out = append(out, r)
		}
	}
	return out
}

func findingKeys(findings []Finding) []string {
	keys := make([]string, len(findings))
	for i, f := range findings {
		b, _ := json.Marshal(f.Events)
		keys[i] = f.RuleID + " " + string(b)
	}
	return keys
}

// benchCommand is an ordinary agent command line.
const benchCommand = "cd /home/user/project/cli && go test ./internal/endpoint/... -run TestX -count=1 2>&1 | tail -50"

func benchRule(b *testing.B, spec, match string) *CompiledRule {
	b.Helper()
	c, err := Compile(&Rule{
		ID: "bench", Spec: spec, Version: 1, Title: "bench",
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit:  Emit{Reason: "x"},
		Match: match,
		Tests: []Fixture{{Name: "x", Verdict: VerdictNoMatch, Events: []FixtureEvent{{Event: commandEvent("ls")}}}},
	})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func benchScan(b *testing.B, rules []*CompiledRule) {
	events := []asymptoteobserve.Event{commandEvent(benchCommand)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ScanEvents(rules, events); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCommandEvent is the cost of evaluating rules on one typical command event: the
// regex curl-pipe-to-shell rule the corpus had before parsed_commands, its parsed_commands
// rewrite, and five rules referencing parsed_commands (which share one parse per event).
func BenchmarkCommandEvent(b *testing.B) {
	regex := `e.event.action == "command.executed" && e.command.command.matches("(?i)\\b(curl|wget)\\b.*\\|\\s*(sudo(\\s+-\\S+)*\\s+)?(ba|z)?sh\\b")`
	parsed := `e.event.action == "command.executed" && (
		e.command.parsed_commands.exists(d,
			d.argv.exists(a, a.matches("(?i)(^|/)(curl|wget)$")) &&
			e.command.parsed_commands.exists(s,
				s.pipeline == d.pipeline && s.stage > d.stage &&
				s.name.matches("(?i)^(ba|z|da|k)?sh$"))) ||
		(size(e.command.parsed_commands) == 0 &&
			e.command.command.matches("(?i)\\b(curl|wget)\\b.*\\|\\s*(sudo(\\s+-\\S+)*\\s+)?(ba|z)?sh\\b")))`
	b.Run("regex_rule", func(b *testing.B) {
		benchScan(b, []*CompiledRule{benchRule(b, "", regex)})
	})
	b.Run("parsed_rule", func(b *testing.B) {
		benchScan(b, []*CompiledRule{benchRule(b, SupportedSpec, parsed)})
	})
	b.Run("five_parsed_rules", func(b *testing.B) {
		var rules []*CompiledRule
		for range 5 {
			rules = append(rules, benchRule(b, SupportedSpec, parsed))
		}
		benchScan(b, rules)
	})
	b.Run("parse_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ParsedCommands(benchCommand)
		}
	})
	// The open corpus's single-event rules, three of which reference parsed_commands.
	b.Run("corpus", func(b *testing.B) {
		rules, err := LoadDir(filepath.Join("..", "..", "..", "rules"))
		if err != nil {
			b.Fatal(err)
		}
		var compiled []*CompiledRule
		for _, r := range rules {
			if r.Correlation != nil {
				continue
			}
			c, err := Compile(r)
			if err != nil {
				b.Fatal(err)
			}
			compiled = append(compiled, c)
		}
		benchScan(b, compiled)
	})
}
