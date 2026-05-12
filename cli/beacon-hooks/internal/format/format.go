package format

import (
	"fmt"
	"strings"
)

// FormatViolation formats a single violation
func FormatViolation(violation map[string]interface{}, index int) string {
	severity := strings.ToUpper(getStringOrDefault(violation, "severity", "unknown"))
	title := getStringOrDefault(violation, "title", "")
	if title == "" {
		title = getStringOrDefault(violation, "policy_name", "Unknown Policy")
	}
	message := getStringOrDefault(violation, "message", "No description")
	remediation := getStringOrDefault(violation, "remediation", "")

	location, _ := violation["location"].(map[string]interface{})
	filePath := getStringOrDefault(location, "file", "unknown")
	line := getIntOrDefault(location, "line_start", 0)

	var lines []string
	lines = append(lines, fmt.Sprintf("### %d. [%s] %s", index, severity, title))
	lines = append(lines, fmt.Sprintf("**File:** `%s` (line %d)", filePath, line))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("**Issue:** %s", message))

	if remediation != "" {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("**Fix:** %s", remediation))
	}

	return strings.Join(lines, "\n")
}

// FormatReport formats a full violation report for re-prompting Claude
func FormatReport(violations []map[string]interface{}) string {
	if len(violations) == 0 {
		return ""
	}

	header := "## Security Vulnerabilities Detected\n\n"
	header += "The following security issues were found in your code changes:\n\n"

	var body []string
	for i, v := range violations {
		body = append(body, FormatViolation(v, i+1))
	}

	footer := "\n\n---\n\nPlease fix these security vulnerabilities before completing the task."

	return header + strings.Join(body, "\n\n---\n\n") + footer
}

// FormatWarning formats a warning message when violations persist after max retries
func FormatWarning(violations []map[string]interface{}) string {
	count := len(violations)
	severityCounts := make(map[string]int)

	for _, v := range violations {
		sev := getStringOrDefault(v, "severity", "unknown")
		severityCounts[sev]++
	}

	var parts []string
	for sev, c := range severityCounts {
		parts = append(parts, fmt.Sprintf("%d %s", c, sev))
	}
	summary := strings.Join(parts, ", ")

	return fmt.Sprintf(
		"Warning: %d security issue(s) (%s) could not be automatically fixed. "+
			"Please review manually before deployment.",
		count, summary)
}

func getStringOrDefault(m map[string]interface{}, key, defaultVal string) string {
	if m == nil {
		return defaultVal
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return defaultVal
}

func getIntOrDefault(m map[string]interface{}, key string, defaultVal int) int {
	if m == nil {
		return defaultVal
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return defaultVal
}
