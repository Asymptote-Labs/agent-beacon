package intel

import (
	"context"
	"os"
	"testing"
)

// TestContractAgainstARealServer runs the client against a real Beacon Intel serving the
// fixture rows in ai-supply-chain-oracle's intel/tests/api/seed.sql, so a field renamed on
// either side fails here rather than decoding to zero values. Opt in with
// BEACON_TEST_INTEL_URL=http://127.0.0.1:3000 (see that repository's intel/README.md, "API").
func TestContractAgainstARealServer(t *testing.T) {
	base := os.Getenv("BEACON_TEST_INTEL_URL")
	if base == "" {
		t.Skip("BEACON_TEST_INTEL_URL is not set")
	}
	normalized, err := NormalizeBaseURL(base)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{BaseURL: normalized, UserAgent: "beacon/contract-test"}
	ctx := context.Background()

	lookup, _, err := c.Lookup(ctx, "Evil Server", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Query.MatchedBy != "text" || len(lookup.Results) == 0 {
		t.Fatalf("lookup = %+v", lookup)
	}
	evil := lookup.Results[0]
	if evil.EntityKey != "mcp:evil/server" || evil.Kind != "mcp" || evil.Name != "Evil Server" || evil.Risk != "malicious" ||
		evil.Guidance == "" || evil.AnalyzedAt == "" || evil.URL == "" || evil.Stars == nil || len(evil.RepositoryFlags) == 0 {
		t.Fatalf("summary = %+v", evil)
	}

	a, _, err := c.Artifact(ctx, "mcp:evil/server")
	if err != nil {
		t.Fatal(err)
	}
	if a.Risk != "malicious" || a.Verdict == nil || a.Verdict.Model != "judge-x" || len(a.Verdict.Findings) != 1 ||
		a.Verdict.Findings[0].ClassLabel == "" || len(a.Verdict.Findings[0].Lines) != 2 || a.Verdict.RefutedFindings != 1 ||
		len(a.Verdict.Reasons) != 1 || a.RepositoryLineage == nil || len(a.RepositoryLineage.Flags) == 0 || a.Note == "" {
		t.Fatalf("artifact = %+v", a)
	}
	if _, _, err := c.Artifact(ctx, "mcp:nope/nope"); !NotFound(err) {
		t.Fatalf("unknown key: %v", err)
	}

	check, _, err := c.Check(ctx, []CheckItem{{Q: "Evil Server"}, {Q: "evil"}, {Q: "anthropics/skills", Kind: "skill"}, {Q: "nope/nothing"}})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []string{}
	for _, r := range check.Results {
		statuses = append(statuses, r.Status+":"+r.Risk)
	}
	if got := statuses; len(got) != 4 || got[0] != "matched:malicious" || got[1] != "ambiguous:" || got[2] != "matched:benign" || got[3] != "not_found:" {
		t.Fatalf("check statuses = %v", got)
	}
	if check.Counts["malicious"] != 1 || check.Counts["not_found"] != 1 || check.Note == "" {
		t.Fatalf("check counts = %v", check.Counts)
	}

	s, _, err := c.Status(ctx)
	if err != nil || s.Index["mcp"].Artifacts == 0 || s.LatestAnalyzedAt == "" {
		t.Fatalf("status = %+v, %v", s, err)
	}
}
