package asymptoteobserve

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file are ceilings on work that is deterministic, so they hold the hook path's
// speed without timing anything. A hook is a process per event that the agent waits on, and both
// of the costs below were paid by every one of them before they were measured.
//
// When a change lowers a measured value well below its ceiling, lower the ceiling with it, so the
// gain cannot be given back unnoticed.

// initAllocCeiling bounds the allocations this package's initialization makes, test files
// included (409 when it was set at 500; 337 when the redaction rules stopped building a
// sync.OnceValue each at init, and it was lowered to 420). Compiling the redaction patterns at
// init made it 11,508: a package-level regexp.MustCompile belongs behind sync.OnceValue.
const initAllocCeiling = 420

func TestPackageInitStaysCheap(t *testing.T) {
	if os.Getenv("BEACON_INIT_TRACE_CHILD") != "" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPackageInitStaysCheap$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1", "BEACON_INIT_TRACE_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("re-run test binary: %v\n%s", err, out)
	}
	match := regexp.MustCompile(`(?m)^init github\.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve @.* (\d+) allocs$`).FindSubmatch(out)
	if match == nil {
		t.Skipf("no init trace for this package in:\n%s", out)
	}
	allocs, _ := strconv.Atoi(string(match[1]))
	if allocs > initAllocCeiling {
		t.Fatalf("package init made %d allocations, ceiling %d; compile patterns and build tables on first use", allocs, initAllocCeiling)
	}
}

// TestOrdinaryTextReachesNoRedactionPattern holds the redaction gate: text with no credential
// shape in it, which is nearly all of what a hook redacts, must not run a single pattern.
func TestOrdinaryTextReachesNoRedactionPattern(t *testing.T) {
	ordinary := []string{
		"info", "tool.invoked", "claude_code", "abc-123", "2026-10-12T03:47:36.515970306Z",
		"/home/user/project/internal/auth/config.go",
		"cd /home/user/project && go test ./... -count=1 2>&1 | tail -50",
		strings.Repeat("ok  \tgithub.com/example/project/internal/pkg\t0.123s  // refreshes the api token cache\n", 100),
		"input_tokens: 812, output_tokens: 90, cache_read_input_tokens: 4000",
		"Please refactor the token refresh logic so the api key is read from the environment.",
		"func load(cfg Config) string { return cfg.Endpoint }",
		"keyboard shortcuts, passport photos, secretary notes",
	}
	for _, text := range ordinary {
		gate := redactGate{value: text, enabled: true}
		if gate.holds(redactLabelNeedles) {
			t.Errorf("%.60q reaches the assignment pattern", text)
		}
		for i, rule := range redactionRules {
			if gate.holds(rule.needles) {
				t.Errorf("%.60q reaches redaction rule %d (%s)", text, i, rule.pattern())
			}
		}
	}
}
