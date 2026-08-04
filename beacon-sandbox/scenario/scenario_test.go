package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Validate exists to catch authoring mistakes *before* a sandbox is paid for, so its reject
// cases matter more than the accept case: a scenario that silently asserts nothing produces a
// green run that means nothing.

func valid() Scenario {
	return Scenario{
		ID:     "t-valid",
		Prompt: "run echo {{canary}}",
		Expect: []Expect{{Action: "command.executed", Fields: []string{"command.command"}}},
	}
}

func TestValidScenarioPasses(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateRejectsAuthoringMistakes(t *testing.T) {
	cases := map[string]func(*Scenario){
		"missing id":            func(s *Scenario) { s.ID = "" },
		"whitespace id":         func(s *Scenario) { s.ID = "   " },
		"missing prompt":        func(s *Scenario) { s.Prompt = "" },
		"whitespace prompt":     func(s *Scenario) { s.Prompt = "\t\n" },
		"no expectations":       func(s *Scenario) { s.Expect = nil },
		"empty expectations":    func(s *Scenario) { s.Expect = []Expect{} },
		"expectation no action": func(s *Scenario) { s.Expect = []Expect{{Fields: []string{"a"}}} },
	}
	for name, mutate := range cases {
		s := valid()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

// A dotted path with an empty segment never resolves, so a typo would make an expectation
// quietly unsatisfiable rather than failing loudly at load time.
func TestValidateRejectsMalformedFieldPaths(t *testing.T) {
	for _, bad := range []string{"", "  ", "command..command", ".command", "command."} {
		s := valid()
		s.Expect[0].Fields = []string{bad}
		err := s.Validate()
		if err == nil {
			t.Errorf("field path %q should be rejected", bad)
			continue
		}
		if !strings.Contains(err.Error(), "malformed field path") {
			t.Errorf("field path %q: error should name the problem, got %v", bad, err)
		}
	}
	// A legitimately nested path must still be accepted.
	s := valid()
	s.Expect[0].Fields = []string{"gen_ai.usage.input_tokens"}
	if err := s.Validate(); err != nil {
		t.Errorf("nested path should be valid, got %v", err)
	}
}

func TestDefaultsAreSane(t *testing.T) {
	var s Scenario
	if got := s.Timeout(); got != 10*time.Minute {
		t.Errorf("default timeout = %v, want 10m", got)
	}
	if got := s.Budget(); got != 1.0 {
		t.Errorf("default budget = %v, want 1.0", got)
	}
	s.TimeoutSeconds = 90
	s.BudgetUSD = 0.25
	if got := s.Timeout(); got != 90*time.Second {
		t.Errorf("explicit timeout = %v, want 90s", got)
	}
	if got := s.Budget(); got != 0.25 {
		t.Errorf("explicit budget = %v, want 0.25", got)
	}
	// A non-positive value must fall back rather than producing a zero budget that would make
	// every session fail instantly.
	s.TimeoutSeconds = -5
	s.BudgetUSD = -1
	if s.Timeout() != 10*time.Minute || s.Budget() != 1.0 {
		t.Errorf("negative values should fall back to defaults, got %v / %v", s.Timeout(), s.Budget())
	}
}

func TestExpandSubstitutesEveryPlaceholder(t *testing.T) {
	got := Expand("marker={{canary}} file={{sentinel}} dir={{workdir}}", "CANARY1", "/tmp/s", "/work")
	want := "marker=CANARY1 file=/tmp/s dir=/work"
	if got != want {
		t.Errorf("Expand = %q, want %q", got, want)
	}
	// Repeated placeholders must all be replaced, since prompts often mention the canary twice.
	if got := Expand("{{canary}} and {{canary}}", "X", "", ""); got != "X and X" {
		t.Errorf("repeated placeholder = %q", got)
	}
	// Unknown placeholders are left alone rather than silently blanked.
	if got := Expand("{{unknown}}", "X", "", ""); got != "{{unknown}}" {
		t.Errorf("unknown placeholder should pass through, got %q", got)
	}
}

func writeScenario(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidatesOnRead(t *testing.T) {
	dir := t.TempDir()
	// Syntactically fine YAML, but asserts nothing.
	writeScenario(t, dir, "bad.yaml", "id: bad\nprompt: hello\n")
	if _, err := Load(filepath.Join(dir, "bad.yaml")); err == nil {
		t.Fatal("Load must validate, not just parse")
	}
	writeScenario(t, dir, "malformed.yaml", "id: [unclosed\n")
	if _, err := Load(filepath.Join(dir, "malformed.yaml")); err == nil {
		t.Fatal("malformed YAML must error")
	}
	if _, err := Load(filepath.Join(dir, "absent.yaml")); err == nil {
		t.Fatal("a missing file must error")
	}
}

func TestLoadSuiteIsDeterministicallyOrdered(t *testing.T) {
	dir := t.TempDir()
	body := "id: %s\nprompt: p {{canary}}\nexpect:\n  - action: prompt.submitted\n"
	// Written out of order on purpose; suite order must not depend on filesystem order.
	for _, id := range []string{"s03", "s01", "s02"} {
		writeScenario(t, dir, id+".yaml", strings.Replace(body, "%s", id, 1))
	}
	suite, err := LoadSuite(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range suite.Scenarios {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "s01,s02,s03" {
		t.Errorf("suite order = %v, want sorted by filename", ids)
	}
}

// An empty directory must error rather than reporting a vacuous pass over zero scenarios.
func TestLoadSuiteRejectsEmptyDirectory(t *testing.T) {
	if _, err := LoadSuite(t.TempDir()); err == nil {
		t.Fatal("an empty suite must error, not silently assert nothing")
	}
}

func TestLoadSuitePropagatesAnInvalidScenario(t *testing.T) {
	dir := t.TempDir()
	writeScenario(t, dir, "ok.yaml", "id: ok\nprompt: p\nexpect:\n  - action: prompt.submitted\n")
	writeScenario(t, dir, "broken.yaml", "id: broken\nprompt: p\n")
	if _, err := LoadSuite(dir); err == nil {
		t.Fatal("one invalid scenario must fail the whole suite load")
	}
}

// The real shipped scenarios must stay valid; this catches a typo in a YAML edit without
// needing a sandbox.
func TestShippedScenariosAreValid(t *testing.T) {
	suite, err := LoadSuite("../scenarios")
	if err != nil {
		t.Fatalf("shipped scenarios failed to load: %v", err)
	}
	if len(suite.Scenarios) == 0 {
		t.Fatal("expected shipped scenarios")
	}
	seen := map[string]bool{}
	for _, s := range suite.Scenarios {
		if seen[s.ID] {
			t.Errorf("duplicate scenario id %q", s.ID)
		}
		seen[s.ID] = true
		// Every expectation should explain itself, or a failure is uninterpretable by whoever
		// reads the verdict.
		for i, e := range s.Expect {
			if strings.TrimSpace(e.Why) == "" {
				t.Errorf("%s: expect[%d] (%s) has no `why`; a failing check must explain what it protects",
					s.ID, i, e.Action)
			}
		}
	}
}

// A setup step that writes the sentinel makes the gate unfalsifiable: the file exists before the
// agent runs, so INCONCLUSIVE can never fire and an idle agent gets misreported as a capture
// failure. s04 shipped with exactly this (`cp fixture {{sentinel}}`) until Cursor Bugbot caught
// it, so the rule is enforced rather than left to review.
func TestSetupMayNotPlantTheSentinel(t *testing.T) {
	sc := Scenario{
		ID:       "bad",
		Prompt:   "do a thing",
		Sentinel: "/home/agent/work/out.txt",
		Setup:    "cp /home/agent/work/fixture/notes.txt {{sentinel}}",
		Expect:   []Expect{{Action: "prompt.submitted", Why: "baseline"}},
	}
	err := sc.Validate()
	if err == nil {
		t.Fatal("a setup that writes the sentinel must be rejected")
	}
	if !strings.Contains(err.Error(), "regardless of what the agent does") {
		t.Errorf("the error should explain why this is unfalsifiable, got: %v", err)
	}
}

// Setup is still allowed to build fixtures the agent will act on -- only the sentinel is off
// limits, or every read-style scenario would become unwritable.
func TestSetupMayStillCreateFixtures(t *testing.T) {
	sc := Scenario{
		ID:       "ok",
		Prompt:   "read the fixture and write the answer to {{sentinel}}",
		Sentinel: "/home/agent/work/answer.txt",
		Setup:    "printf 'line {{canary}}\\n' > /home/agent/work/fixture/notes.txt",
		Expect:   []Expect{{Action: "file.read", Why: "the point of the scenario"}},
	}
	if err := sc.Validate(); err != nil {
		t.Errorf("a fixture-building setup must remain valid: %v", err)
	}
}

// The guard must catch every spelling of "setup creates the sentinel", not just the placeholder.
// A first version matched only the literal "{{sentinel}}" string, so a setup writing the real
// path or assembling it from {{workdir}} walked straight past — leaving the same unfalsifiable
// gate the guard exists to prevent. Cursor Bugbot flagged the gap.
func TestSentinelGuardCatchesEverySpelling(t *testing.T) {
	cases := map[string]string{
		"placeholder":      "cp fixture.txt {{sentinel}}",
		"literal path":     "cp fixture.txt /home/agent/work/planted.txt",
		"workdir-relative": "cp fixture.txt {{workdir}}/planted.txt",
		"redirect":         "printf hi > /home/agent/work/planted.txt",
		"touch":            "touch /home/agent/work/planted.txt",
	}
	for name, setup := range cases {
		sc := Scenario{
			ID:       "bad-" + name,
			Prompt:   "do a thing",
			Sentinel: "/home/agent/work/planted.txt",
			Setup:    setup,
			Expect:   []Expect{{Action: "prompt.submitted", Why: "baseline"}},
		}
		err := sc.Validate()
		if err == nil {
			t.Errorf("%s: setup %q plants the sentinel and must be rejected", name, setup)
			continue
		}
		if !strings.Contains(err.Error(), "regardless of what the agent does") {
			t.Errorf("%s: error should explain the unfalsifiability, got: %v", name, err)
		}
	}
}

// Setup must still be free to build fixtures the agent acts on, or read-style scenarios become
// unwritable. Only the sentinel itself is off limits.
func TestSentinelGuardAllowsUnrelatedFixtures(t *testing.T) {
	sc := Scenario{
		ID:       "ok",
		Prompt:   "read the fixture and write the answer to {{sentinel}}",
		Sentinel: "/home/agent/work/answer.txt",
		Setup:    "printf 'line {{canary}}\\n' > /home/agent/work/fixture/notes.txt",
		Expect:   []Expect{{Action: "file.read", Why: "the point of the scenario"}},
	}
	if err := sc.Validate(); err != nil {
		t.Errorf("an unrelated fixture must remain valid: %v", err)
	}
}

// The basename check has to respect path boundaries. A bare substring test rejected valid setups:
// a sentinel named out.txt matched a fixture called timeout.txt, so a legitimate scenario could
// not be written at all. Cursor Bugbot caught the false positive.
func TestSentinelBasenameMatchRespectsPathBoundaries(t *testing.T) {
	valid := map[string]string{
		"longer name":       "printf hi > /home/agent/work/timeout.txt",
		"different prefix":  "printf hi > /home/agent/work/layout.txt",
		"suffixed":          "printf hi > /home/agent/work/out.txt.bak",
		"embedded in a dir": "mkdir -p /home/agent/work/out.txt.d",
	}
	for name, setup := range valid {
		sc := Scenario{
			ID: "ok-" + name, Prompt: "write to {{sentinel}}",
			Sentinel: "/home/agent/work/out.txt", Setup: setup,
			Expect: []Expect{{Action: "file.modified", Why: "the point"}},
		}
		if err := sc.Validate(); err != nil {
			t.Errorf("%s: setup %q does not plant out.txt and must be allowed: %v", name, setup, err)
		}
	}

	// The real thing must still be caught, by any spelling.
	planting := map[string]string{
		"exact basename": "printf hi > /home/agent/work/out.txt",
		"workdir form":   "printf hi > {{workdir}}/out.txt",
		"quoted":         "cp fixture 'out.txt'",
	}
	for name, setup := range planting {
		sc := Scenario{
			ID: "bad-" + name, Prompt: "write to {{sentinel}}",
			Sentinel: "/home/agent/work/out.txt", Setup: setup,
			Expect: []Expect{{Action: "file.modified", Why: "the point"}},
		}
		if err := sc.Validate(); err == nil {
			t.Errorf("%s: setup %q plants the sentinel and must be rejected", name, setup)
		}
	}
}

// An install scenario reaches the guest as CLI flags, so a typo would fail mid-run after a
// sandbox has already been paid for. Validate catches it while it is still free.
func TestInstallModeAndServiceAreValidated(t *testing.T) {
	base := func(in *Install) Scenario {
		return Scenario{ID: "i", Prompt: "do a thing", Install: in,
			Expect: []Expect{{Action: "prompt.submitted", Why: "baseline"}}}
	}
	for _, in := range []*Install{
		{Mode: "systemwide"},
		{Mode: "root"},
		{Service: "launchd2"},
		{Service: "upstart"},
	} {
		if err := base(in).Validate(); err == nil {
			t.Errorf("install %+v must be rejected", in)
		}
	}
	// Every documented spelling must be accepted, including the empty auto-detect default.
	for _, in := range []*Install{
		{},
		{Mode: "user"},
		{Mode: "system", Service: "systemd"},
		{Service: "none"},
		{Service: "auto"},
		{Mode: "user", Service: "launchd", ExpectStatusRunning: true},
	} {
		if err := base(in).Validate(); err != nil {
			t.Errorf("install %+v must be accepted: %v", in, err)
		}
	}
}
