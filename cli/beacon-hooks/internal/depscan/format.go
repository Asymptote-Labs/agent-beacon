package depscan

import (
	"fmt"
	"strings"
)

// PackageFinding represents a deduplicated set of vulnerabilities for one package.
type PackageFinding struct {
	Pkg        DetectedPackage
	CVEs       []string
	Severity   string
	Summary    string
	FixVersion string
}

// severityRank maps severity strings to numeric ranks for comparison.
var severityRank = map[string]int{
	"LOW":      1,
	"MODERATE": 2,
	"HIGH":     3,
	"CRITICAL": 4,
}

// DeduplicateFindings groups vulnerabilities by package key and picks the highest
// severity, highest fix version, and collects all CVE IDs.
func DeduplicateFindings(pkgs []DetectedPackage, findings map[string][]OSVVulnerability) []PackageFinding {
	// Build a map from package key to the DetectedPackage
	pkgMap := make(map[string]DetectedPackage)
	for _, p := range pkgs {
		pkgMap[p.Name+"@"+p.Version] = p
	}

	var result []PackageFinding

	for key, vulns := range findings {
		pkg := pkgMap[key]
		finding := PackageFinding{Pkg: pkg}

		var bestRank int
		var bestFixVersion string

		for _, v := range vulns {
			// Collect CVE aliases
			for _, alias := range v.Aliases {
				if strings.HasPrefix(alias, "CVE-") {
					finding.CVEs = append(finding.CVEs, alias)
				}
			}

			// If no CVE alias, use the OSV ID
			if len(v.Aliases) == 0 || !hasCVE(v.Aliases) {
				finding.CVEs = append(finding.CVEs, v.ID)
			}

			// Track highest severity
			sev := strings.ToUpper(v.Severity)
			rank := severityRank[sev]
			if rank > bestRank {
				bestRank = rank
				finding.Severity = sev
				finding.Summary = v.Summary
			}

			// Track highest fix version
			if v.FixedVersion != "" && compareVersions(v.FixedVersion, bestFixVersion) > 0 {
				bestFixVersion = v.FixedVersion
			}
		}

		if finding.Severity == "" {
			finding.Severity = "UNKNOWN"
			if len(vulns) > 0 {
				finding.Summary = vulns[0].Summary
			}
		}

		finding.FixVersion = bestFixVersion

		// Deduplicate CVE IDs
		finding.CVEs = uniqueStrings(finding.CVEs)

		result = append(result, finding)
	}

	return result
}

// FormatCVEReport generates a markdown report for injection into the agent's context.
func FormatCVEReport(findings []PackageFinding) string {
	if len(findings) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Vulnerable Dependencies Detected\n\n")
	sb.WriteString("The following dependencies have known security vulnerabilities:\n\n")

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("### %d. [%s] %s %s\n", i+1, f.Severity, f.Pkg.Name, f.Pkg.Version))
		sb.WriteString(fmt.Sprintf("**File:** `%s`\n", f.Pkg.FilePath))
		sb.WriteString(fmt.Sprintf("**CVEs:** %s\n\n", strings.Join(f.CVEs, ", ")))
		sb.WriteString(fmt.Sprintf("**Vulnerability:** %s\n\n", f.Summary))

		if f.FixVersion != "" {
			sb.WriteString(fmt.Sprintf("**Fix:** Upgrade to %s >= %s\n", f.Pkg.Name, f.FixVersion))
		}

		if i < len(findings)-1 {
			sb.WriteString("\n---\n\n")
		}
	}

	sb.WriteString("\n\n---\n\nPlease update these dependencies to non-vulnerable versions before completing the task.")

	return sb.String()
}

// hasCVE checks if any alias starts with "CVE-".
func hasCVE(aliases []string) bool {
	for _, a := range aliases {
		if strings.HasPrefix(a, "CVE-") {
			return true
		}
	}
	return false
}

// uniqueStrings deduplicates a string slice preserving order.
func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// compareVersions does a simple string-based version comparison.
// Returns >0 if a > b, <0 if a < b, 0 if equal.
// Uses lexicographic comparison of dot-separated segments.
func compareVersions(a, b string) int {
	if b == "" {
		return 1
	}
	if a == "" {
		return -1
	}

	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var pA, pB string
		if i < len(partsA) {
			pA = partsA[i]
		}
		if i < len(partsB) {
			pB = partsB[i]
		}

		// Try numeric comparison
		nA := parseNum(pA)
		nB := parseNum(pB)
		if nA != nB {
			return nA - nB
		}
	}
	return 0
}

// parseNum parses a numeric string; returns 0 for non-numeric.
func parseNum(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			break
		}
	}
	return n
}
