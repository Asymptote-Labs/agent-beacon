package version

import "testing"

func TestEditionIsReportedOnlyWhenNotDefault(t *testing.T) {
	origVersion, origCommit, origDate, origEdition := Version, GitCommit, BuildDate, Edition
	t.Cleanup(func() {
		Version, GitCommit, BuildDate, Edition = origVersion, origCommit, origDate, origEdition
	})
	Version, GitCommit, BuildDate = "1.2.3", "abc1234", "2026-10-13"

	Edition = DefaultEdition
	if got := GetFullVersion(); got != "1.2.3 (abc1234) built on 2026-10-13" {
		t.Fatalf("default edition changed the version line: %q", got)
	}
	if got := ReportedEdition(); got != "" {
		t.Fatalf("ReportedEdition() = %q for the default edition", got)
	}

	Edition = "partner"
	if got := GetFullVersion(); got != "1.2.3 (abc1234) built on 2026-10-13, partner edition" {
		t.Fatalf("GetFullVersion() = %q", got)
	}
	if got := ReportedEdition(); got != "partner" {
		t.Fatalf("ReportedEdition() = %q", got)
	}
}
