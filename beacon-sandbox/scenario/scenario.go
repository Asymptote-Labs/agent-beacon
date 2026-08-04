// Package scenario defines what to ask the agent and what Beacon must then show.
//
// The checking model is deliberately small: plant a unique canary in the prompt, have the
// command leave a sentinel file, then assert Beacon's log contains events matching the
// declared expectations. The canary makes matching exact; the sentinel makes a missing
// event unambiguous -- if the agent never acted there is nothing for Beacon to have missed,
// so the run is INCONCLUSIVE rather than FAIL.
package scenario

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Expect is one assertion against the captured event stream.
type Expect struct {
	// Action matches event.action exactly, e.g. "command.executed".
	Action string `yaml:"action"`
	// Contains requires these substrings somewhere in the matched event's JSON. Use
	// {{canary}} to reference the run's canary.
	Contains []string `yaml:"contains,omitempty"`
	// Fields requires these dotted paths to be present and non-empty, e.g.
	// "command.command" or "file.path". This is how we assert the field population that
	// threat rules depend on, rather than just event presence.
	Fields []string `yaml:"fields,omitempty"`
	// MinCount defaults to 1.
	MinCount int `yaml:"min_count,omitempty"`
	// Optional records the expectation without failing the run. Use for signals that
	// are known-missing and tracked in BACKLOG.md, so a regression is still visible.
	Optional bool `yaml:"optional,omitempty"`
	// Why explains what this expectation protects, and is printed on failure.
	Why string `yaml:"why,omitempty"`
}

// Install describes a persistent `beacon endpoint install` to perform before the session,
// instead of the default ephemeral `beacon ci exec` collector.
//
// This is what exercises the parity surface: service manager selection, unit installation,
// status reporting, and whether a real installed collector actually receives telemetry. A
// scenario without it tests capture through the CI path, which is a different shipping path
// and cannot tell you whether `endpoint install` works at all.
type Install struct {
	// Mode is "user" (default) or "system". System mode runs as root.
	Mode string `yaml:"mode,omitempty"`
	// Service selects the service manager: empty auto-detects, or "systemd" / "none".
	Service string `yaml:"service,omitempty"`
	// ExpectStatusRunning requires the collector service to report running after install.
	ExpectStatusRunning bool `yaml:"expect_status_running,omitempty"`
	// ExpectServiceKind requires the endpoint to report this service backend after install.
	//
	// Which backend got selected is the substance of a service-manager scenario, and it is not
	// implied by "running": the supervised fallback also reports running, so a systemd scenario
	// on a host without systemd would otherwise pass while testing the wrong thing.
	ExpectServiceKind string `yaml:"expect_service_kind,omitempty"`
	// NeedsRealSystemd runs the install inside a nested privileged container where systemd is
	// genuinely PID 1.
	//
	// The sandbox provider cannot offer that directly: its own init holds PID 1, so systemctl
	// refuses. Rather than make a scenario describe VM lanes and Docker, it states the
	// requirement and the runner arranges it.
	NeedsRealSystemd bool `yaml:"needs_real_systemd,omitempty"`
}

// Scenario is one declarative case.
type Scenario struct {
	ID     string `yaml:"id"`
	Prompt string `yaml:"prompt"`
	// Install, when set, performs a persistent endpoint install and runs the session against
	// it, with no `beacon ci exec` wrapper anywhere.
	Install *Install `yaml:"install,omitempty"`
	// Sentinel is a path the agent's work must create. Its presence proves the agent
	// acted; its absence makes the run inconclusive.
	Sentinel string `yaml:"sentinel,omitempty"`
	// Setup runs as the agent user before the session, for planting fixtures.
	Setup string `yaml:"setup,omitempty"`
	// Tools restricts Claude Code's tool set, tightening what a run can do.
	Tools []string `yaml:"tools,omitempty"`
	// DisallowedTools forces denials, exercising the approval path.
	DisallowedTools []string `yaml:"disallowed_tools,omitempty"`
	// Cwd is relative to the work dir.
	Cwd string `yaml:"cwd,omitempty"`
	// BudgetUSD caps spend for the session.
	BudgetUSD float64 `yaml:"budget_usd,omitempty"`
	// TimeoutSeconds bounds the session.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
	// BlockEgress denies all outbound network. Only useful for scenarios that need no
	// model call, since Claude Code needs the API.
	BlockEgress bool     `yaml:"block_egress,omitempty"`
	Expect      []Expect `yaml:"expect"`
}

// Suite is an ordered set of scenarios loaded from a directory.
type Suite struct {
	Name      string
	Scenarios []Scenario
}

// Timeout resolves the session bound with a sane default.
func (s Scenario) Timeout() time.Duration {
	if s.TimeoutSeconds > 0 {
		return time.Duration(s.TimeoutSeconds) * time.Second
	}
	return 10 * time.Minute
}

// Budget resolves the spend cap with a sane default.
func (s Scenario) Budget() float64 {
	if s.BudgetUSD > 0 {
		return s.BudgetUSD
	}
	return 1.0
}

// Validate catches scenario authoring mistakes before a sandbox is paid for.
func (s Scenario) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("scenario is missing id")
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("%s: prompt is required", s.ID)
	}
	if len(s.Expect) == 0 {
		return fmt.Errorf("%s: at least one expectation is required, else the run asserts nothing", s.ID)
	}
	for i, e := range s.Expect {
		if strings.TrimSpace(e.Action) == "" {
			return fmt.Errorf("%s: expect[%d] is missing action", s.ID, i)
		}
		// An unrecognised mode or service would reach the guest as a bad flag and fail mid-run,
		// after a sandbox has already been paid for. Catch it here instead.
		if s.Install != nil {
			switch s.Install.Mode {
			case "", "user", "system":
			default:
				return fmt.Errorf("%s: install.mode %q must be user or system", s.ID, s.Install.Mode)
			}
			// A systemd requirement with the supervised backend, or without system mode, would
			// silently exercise something other than what the scenario claims to test.
			if s.Install.NeedsRealSystemd && s.Install.Service == "none" {
				return fmt.Errorf("%s: needs_real_systemd with service=none is contradictory; "+
					"the supervised backend does not use systemd", s.ID)
			}
			if s.Install.NeedsRealSystemd && s.Install.Mode != "system" {
				return fmt.Errorf("%s: needs_real_systemd requires mode=system; a user unit in a "+
					"container has no logind session to attach to", s.ID)
			}
			switch s.Install.ExpectServiceKind {
			case "", "systemd", "launchd", "none":
			default:
				return fmt.Errorf("%s: install.expect_service_kind %q must be systemd, launchd or none",
					s.ID, s.Install.ExpectServiceKind)
			}
			switch s.Install.Service {
			case "", "auto", "systemd", "launchd", "none":
			default:
				return fmt.Errorf("%s: install.service %q must be auto, systemd, launchd, or none",
					s.ID, s.Install.Service)
			}
		}

		// A setup step that writes the sentinel makes the gate unfalsifiable: the file is present
		// before the agent runs, so INCONCLUSIVE can never fire and an idle agent is misreported as
		// a capture failure. s04 shipped with exactly this mistake, so the rule is enforced here
		// rather than left to review.
		//
		// Checked after expansion, not on the raw text. A first version matched only the literal
		// "{{sentinel}}" placeholder, which a setup writing the sentinel's real path -- or building
		// it from {{workdir}} -- walked straight past. Cursor Bugbot pointed out that the guard was
		// narrower than the rule it claimed to enforce.
		if s.Sentinel != "" && s.Setup != "" {
			if target, why := setupPlantsSentinel(s.Setup, s.Sentinel); target != "" {
				return fmt.Errorf("%s: setup %s, which makes the sentinel present regardless of "+
					"what the agent does; the sentinel must be produced by the agent", s.ID, why)
			}
		}

		// A dotted path with an empty segment silently never resolves, so a typo like
		// "command..command" would make an expectation quietly unsatisfiable rather than
		// failing loudly here.
		for _, f := range e.Fields {
			if strings.TrimSpace(f) == "" || strings.Contains(f, "..") ||
				strings.HasPrefix(f, ".") || strings.HasSuffix(f, ".") {
				return fmt.Errorf("%s: expect[%d] has malformed field path %q", s.ID, i, f)
			}
		}
	}
	return nil
}

// Load reads one scenario file.
func Load(path string) (Scenario, error) {
	var s Scenario
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := yaml.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	return s, nil
}

// LoadSuite reads every *.yaml in dir, sorted by filename so ordering is deterministic.
func LoadSuite(dir string) (Suite, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return Suite{}, err
	}
	if len(paths) == 0 {
		return Suite{}, fmt.Errorf("no scenarios found in %s", dir)
	}
	suite := Suite{Name: filepath.Base(dir)}
	for _, p := range paths {
		s, err := Load(p)
		if err != nil {
			return Suite{}, err
		}
		suite.Scenarios = append(suite.Scenarios, s)
	}
	return suite, nil
}

// Expand substitutes run-scoped placeholders into a template string.
func Expand(tmpl string, canary, sentinel, workdir string) string {
	r := strings.NewReplacer(
		"{{canary}}", canary,
		"{{sentinel}}", sentinel,
		"{{workdir}}", workdir,
	)
	return r.Replace(tmpl)
}

// setupPlantsSentinel reports whether a setup step would create the sentinel before the session.
//
// Expansion happens first so the three equivalent spellings are all caught: the {{sentinel}}
// placeholder, the sentinel's literal path, and a path assembled from {{workdir}}. The basename
// is also checked, because "{{workdir}}/x" and a sentinel of "/home/agent/work/x" only agree
// once the real workdir is known, which this package deliberately does not import.
func setupPlantsSentinel(setup, sentinel string) (target, why string) {
	const (
		probeCanary  = "PROBE_CANARY"
		probeWorkdir = "PROBE_WORKDIR"
	)
	sent := Expand(sentinel, probeCanary, "", probeWorkdir)
	exp := Expand(setup, probeCanary, sent, probeWorkdir)

	// Boundary-aware for the full path too, not just the basename: a plain substring test made
	// "> /home/agent/work/out.txt.bak" look like it wrote /home/agent/work/out.txt.
	if sent != "" && containsPathElement(exp, sent) {
		return sent, fmt.Sprintf("writes the sentinel path %q", sent)
	}
	// A trailing path element match catches {{workdir}}-relative spellings of the same file, but
	// it has to respect path boundaries. A bare substring test rejected valid setups: a sentinel
	// named out.txt matched a fixture called timeout.txt or layout.txt, so a legitimate scenario
	// could not be written at all. Cursor Bugbot caught the false positive.
	if base := path.Base(sent); base != "" && base != "." && base != "/" {
		if containsPathElement(exp, base) {
			return base, fmt.Sprintf("writes %q, the sentinel's file name", base)
		}
	}
	return "", ""
}

// containsPathElement reports whether name appears in text as a whole final path element rather
// than as an arbitrary substring, so "out.txt" does not match "timeout.txt".
func containsPathElement(text, name string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(name)
		// The character before must not extend the file name -- a separator or any
		// non-filename character (space, quote, redirect) is a real boundary.
		leftOK := start == 0 || !isFilenameByte(text[start-1])
		// The character after must not extend it either, so "out.txt" does not match
		// "out.txt.bak".
		rightOK := end == len(text) || !isFilenameByte(text[end])
		if leftOK && rightOK {
			return true
		}
		i = start + 1
	}
}

func isFilenameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '.' || b == '_' || b == '-':
		return true
	}
	return false
}

// NeedsRealSystemd reports whether this scenario must run somewhere systemd is genuinely PID 1.
func (s Scenario) NeedsRealSystemd() bool {
	return s.Install != nil && s.Install.NeedsRealSystemd
}
