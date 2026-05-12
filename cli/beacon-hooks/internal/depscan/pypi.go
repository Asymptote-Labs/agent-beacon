package depscan

import (
	"regexp"
	"strings"
)

// requirementPattern matches pip requirement specifiers like:
//
//	pkg==1.2.3, pkg>=1.2.3, pkg~=1.2.3, pkg<=1.2.3
var requirementPattern = regexp.MustCompile(`^\s*([a-zA-Z0-9][a-zA-Z0-9._-]*)\s*(?:==|>=|~=|<=)\s*([0-9][0-9a-zA-Z.*-]*)\s*(?:[;#].*)?$`)

// ParseRequirementsTxt parses added lines from requirements.txt, setup.py, or Pipfile
// for package==version patterns.
func ParseRequirementsTxt(addedLines []string) []DetectedPackage {
	var packages []DetectedPackage
	for _, line := range addedLines {
		line = strings.TrimSpace(line)

		// Strip inline comments before processing
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}

		// Skip empty lines and options (like -r, --index-url)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}

		// For Pipfile lines like: pkg = "==1.2.3"
		if strings.Contains(line, `= "`) {
			line = parsePipfileLine(line)
			if line == "" {
				continue
			}
		}

		matches := requirementPattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		packages = append(packages, DetectedPackage{
			Name:    normalizePackageName(matches[1]),
			Version: matches[2],
		})
	}
	return packages
}

// pyprojectDepPattern matches dependency entries in pyproject.toml like:
//
//	"jinja2>=3.1.4", "requests==2.31.0",
var pyprojectDepPattern = regexp.MustCompile(`"([a-zA-Z0-9][a-zA-Z0-9._-]*)\s*(?:==|>=|~=|<=)\s*([0-9][0-9a-zA-Z.*-]*)"`)

// ParsePyprojectToml parses added lines from pyproject.toml for dependency entries.
func ParsePyprojectToml(addedLines []string) []DetectedPackage {
	var packages []DetectedPackage
	for _, line := range addedLines {
		matches := pyprojectDepPattern.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			packages = append(packages, DetectedPackage{
				Name:    normalizePackageName(match[1]),
				Version: match[2],
			})
		}
	}
	return packages
}

// normalizePackageName normalizes a Python package name (PEP 503).
// Replaces hyphens and dots with underscores and lowercases.
func normalizePackageName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}

// parsePipfileLine converts a Pipfile line like `pkg = "==1.2.3"` to `pkg==1.2.3`.
func parsePipfileLine(line string) string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	pkg := strings.TrimSpace(parts[0])
	version := strings.TrimSpace(parts[1])
	// Remove surrounding quotes
	version = strings.Trim(version, `"'`)
	if version == "*" || version == "" {
		return ""
	}
	return pkg + version
}
