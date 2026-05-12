package depscan

import (
	"strings"
	"testing"
)

func TestConvertVuln(t *testing.T) {
	v := osvVuln{
		ID:      "GHSA-abc-123",
		Summary: "Test vulnerability",
		Aliases: []string{"CVE-2025-12345"},
		DatabaseSpecific: &osvDatabaseSpecific{
			Severity: "HIGH",
		},
		Affected: []osvAffected{
			{
				Ranges: []osvRange{
					{
						Events: []osvEvent{
							{Introduced: "0"},
							{Fixed: "3.1.6"},
						},
					},
				},
			},
		},
	}

	result := convertVuln(v)

	if result.ID != "GHSA-abc-123" {
		t.Errorf("ID = %q, want GHSA-abc-123", result.ID)
	}
	if result.Summary != "Test vulnerability" {
		t.Errorf("Summary = %q, want 'Test vulnerability'", result.Summary)
	}
	if result.Severity != "HIGH" {
		t.Errorf("Severity = %q, want HIGH", result.Severity)
	}
	if result.FixedVersion != "3.1.6" {
		t.Errorf("FixedVersion = %q, want 3.1.6", result.FixedVersion)
	}
	if len(result.Aliases) != 1 || result.Aliases[0] != "CVE-2025-12345" {
		t.Errorf("Aliases = %v, want [CVE-2025-12345]", result.Aliases)
	}
}

func TestConvertVuln_NoSeverity(t *testing.T) {
	v := osvVuln{
		ID:      "GHSA-xyz",
		Summary: "No severity info",
	}

	result := convertVuln(v)

	if result.Severity != "" {
		t.Errorf("Severity = %q, want empty", result.Severity)
	}
	if result.FixedVersion != "" {
		t.Errorf("FixedVersion = %q, want empty", result.FixedVersion)
	}
}

func TestConvertVuln_NilDatabaseSpecific(t *testing.T) {
	v := osvVuln{
		ID:               "GHSA-nil",
		DatabaseSpecific: nil,
	}

	result := convertVuln(v)

	if result.Severity != "" {
		t.Errorf("Severity = %q, want empty", result.Severity)
	}
}

func TestConvertVuln_MultipleFixedVersions(t *testing.T) {
	// When there are multiple fixed versions, the last one wins
	v := osvVuln{
		ID: "GHSA-multi",
		Affected: []osvAffected{
			{
				Ranges: []osvRange{
					{
						Events: []osvEvent{
							{Introduced: "0"},
							{Fixed: "3.1.5"},
						},
					},
					{
						Events: []osvEvent{
							{Introduced: "4.0.0"},
							{Fixed: "4.0.2"},
						},
					},
				},
			},
		},
	}

	result := convertVuln(v)

	if result.FixedVersion != "4.0.2" {
		t.Errorf("FixedVersion = %q, want 4.0.2", result.FixedVersion)
	}
}

func TestSeverityFilter(t *testing.T) {
	// Simulate what QueryPackage does after converting vulns
	vulns := []osvVuln{
		{ID: "v1", Summary: "Critical", DatabaseSpecific: &osvDatabaseSpecific{Severity: "CRITICAL"}},
		{ID: "v2", Summary: "High", DatabaseSpecific: &osvDatabaseSpecific{Severity: "HIGH"}},
		{ID: "v3", Summary: "Moderate", DatabaseSpecific: &osvDatabaseSpecific{Severity: "MODERATE"}},
		{ID: "v4", Summary: "Low", DatabaseSpecific: &osvDatabaseSpecific{Severity: "LOW"}},
		{ID: "v5", Summary: "No severity", DatabaseSpecific: nil},
		{ID: "v6", Summary: "Empty severity", DatabaseSpecific: &osvDatabaseSpecific{Severity: ""}},
	}

	var filtered []OSVVulnerability
	for _, v := range vulns {
		vuln := convertVuln(v)
		sev := strings.ToUpper(vuln.Severity)
		if sev != "HIGH" && sev != "CRITICAL" {
			continue
		}
		filtered = append(filtered, vuln)
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered vulns, got %d", len(filtered))
	}
	if filtered[0].ID != "v1" {
		t.Errorf("filtered[0].ID = %q, want v1", filtered[0].ID)
	}
	if filtered[1].ID != "v2" {
		t.Errorf("filtered[1].ID = %q, want v2", filtered[1].ID)
	}
}
