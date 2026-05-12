package depscan

import (
	"strings"
	"testing"
)

func TestDeduplicateFindings(t *testing.T) {
	pkgs := []DetectedPackage{
		{Name: "jinja2", Version: "3.1.4", Ecosystem: "PyPI", FilePath: "requirements.txt"},
	}

	findings := map[string][]OSVVulnerability{
		"jinja2@3.1.4": {
			{
				ID:           "GHSA-abc",
				Summary:      "Sandbox escape",
				Aliases:      []string{"CVE-2025-27516"},
				Severity:     "MODERATE",
				FixedVersion: "3.1.5",
			},
			{
				ID:           "GHSA-xyz",
				Summary:      "Critical issue",
				Aliases:      []string{"CVE-2024-56326"},
				Severity:     "HIGH",
				FixedVersion: "3.1.6",
			},
		},
	}

	result := DeduplicateFindings(pkgs, findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}

	f := result[0]
	if f.Severity != "HIGH" {
		t.Errorf("severity = %q, want HIGH", f.Severity)
	}
	if f.FixVersion != "3.1.6" {
		t.Errorf("fix version = %q, want 3.1.6", f.FixVersion)
	}
	if len(f.CVEs) != 2 {
		t.Errorf("expected 2 CVEs, got %d", len(f.CVEs))
	}
	if f.Summary != "Critical issue" {
		t.Errorf("summary = %q, want %q", f.Summary, "Critical issue")
	}
}

func TestFormatCVEReport(t *testing.T) {
	findings := []PackageFinding{
		{
			Pkg:        DetectedPackage{Name: "jinja2", Version: "3.1.4", FilePath: "requirements.txt"},
			CVEs:       []string{"CVE-2025-27516", "CVE-2024-56326"},
			Severity:   "HIGH",
			Summary:    "Sandbox breakout",
			FixVersion: "3.1.6",
		},
	}

	report := FormatCVEReport(findings)

	if !strings.Contains(report, "Vulnerable Dependencies Detected") {
		t.Error("report missing header")
	}
	if !strings.Contains(report, "[HIGH] jinja2 3.1.4") {
		t.Error("report missing severity/package info")
	}
	if !strings.Contains(report, "CVE-2025-27516") {
		t.Error("report missing CVE ID")
	}
	if !strings.Contains(report, "jinja2 >= 3.1.6") {
		t.Error("report missing fix version")
	}
	if !strings.Contains(report, "Sandbox breakout") {
		t.Error("report missing summary")
	}
	if !strings.Contains(report, "Please update these dependencies") {
		t.Error("report missing footer")
	}
}

func TestFormatCVEReport_Empty(t *testing.T) {
	report := FormatCVEReport(nil)
	if report != "" {
		t.Errorf("expected empty report, got %q", report)
	}
}

func TestDeduplicateFindings_NoSeverity(t *testing.T) {
	pkgs := []DetectedPackage{
		{Name: "unknown-pkg", Version: "1.0.0", Ecosystem: "PyPI", FilePath: "requirements.txt"},
	}

	findings := map[string][]OSVVulnerability{
		"unknown-pkg@1.0.0": {
			{ID: "GHSA-xyz", Summary: "Some vuln", Severity: "", FixedVersion: "1.0.1"},
		},
	}

	result := DeduplicateFindings(pkgs, findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}
	if result[0].Severity != "UNKNOWN" {
		t.Errorf("severity = %q, want UNKNOWN", result[0].Severity)
	}
}

func TestDeduplicateFindings_NoCVEAlias(t *testing.T) {
	pkgs := []DetectedPackage{
		{Name: "pkg", Version: "1.0.0", Ecosystem: "PyPI", FilePath: "requirements.txt"},
	}

	findings := map[string][]OSVVulnerability{
		"pkg@1.0.0": {
			{ID: "GHSA-only-id", Summary: "No CVE alias", Severity: "HIGH", Aliases: []string{}},
		},
	}

	result := DeduplicateFindings(pkgs, findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}
	// Should fall back to OSV ID when no CVE alias exists
	if len(result[0].CVEs) != 1 || result[0].CVEs[0] != "GHSA-only-id" {
		t.Errorf("CVEs = %v, want [GHSA-only-id]", result[0].CVEs)
	}
}

func TestFormatCVEReport_NoFixVersion(t *testing.T) {
	findings := []PackageFinding{
		{
			Pkg:      DetectedPackage{Name: "pkg", Version: "1.0.0", FilePath: "requirements.txt"},
			CVEs:     []string{"CVE-2025-99999"},
			Severity: "HIGH",
			Summary:  "A vulnerability",
		},
	}

	report := FormatCVEReport(findings)

	if strings.Contains(report, "**Fix:**") {
		t.Error("report should not contain Fix section when no fix version")
	}
	if !strings.Contains(report, "CVE-2025-99999") {
		t.Error("report missing CVE ID")
	}
}

func TestFormatCVEReport_MultipleFindings(t *testing.T) {
	findings := []PackageFinding{
		{
			Pkg:        DetectedPackage{Name: "pkg1", Version: "1.0.0", FilePath: "requirements.txt"},
			CVEs:       []string{"CVE-2025-00001"},
			Severity:   "HIGH",
			Summary:    "First vuln",
			FixVersion: "1.0.1",
		},
		{
			Pkg:        DetectedPackage{Name: "pkg2", Version: "2.0.0", FilePath: "requirements.txt"},
			CVEs:       []string{"CVE-2025-00002"},
			Severity:   "CRITICAL",
			Summary:    "Second vuln",
			FixVersion: "2.0.1",
		},
	}

	report := FormatCVEReport(findings)

	if !strings.Contains(report, "### 1.") || !strings.Contains(report, "### 2.") {
		t.Error("report should have numbered findings")
	}
	if !strings.Contains(report, "---") {
		t.Error("report should have separator between findings")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int // >0, 0, or <0
	}{
		{"3.1.6", "3.1.5", 1},
		{"3.1.5", "3.1.6", -1},
		{"3.1.6", "3.1.6", 0},
		{"4.0.0", "3.9.9", 1},
		{"3.1.6", "", 1},
		{"", "3.1.6", -1},
	}

	for _, tt := range tests {
		result := compareVersions(tt.a, tt.b)
		if (tt.want > 0 && result <= 0) || (tt.want < 0 && result >= 0) || (tt.want == 0 && result != 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tt.a, tt.b, result, tt.want)
		}
	}
}
