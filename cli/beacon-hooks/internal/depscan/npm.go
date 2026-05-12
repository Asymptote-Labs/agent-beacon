package depscan

import (
	"regexp"
	"strings"
)

// npmDepPattern matches package.json dependency entries like:
//
//	"express": "4.18.2"
//	"lodash": "^4.17.21"
//	"react": "~18.2.0"
//
// Captures: package name, optional prefix (^, ~), version
var npmDepPattern = regexp.MustCompile(`"([a-zA-Z0-9@/._-]+)"\s*:\s*"([~^]?)(\d+\.\d+\.\d+[a-zA-Z0-9._-]*)"`)

// ParsePackageJSON parses added lines from package.json for dependency entries.
func ParsePackageJSON(addedLines []string) []DetectedPackage {
	var packages []DetectedPackage
	for _, line := range addedLines {
		line = strings.TrimSpace(line)

		matches := npmDepPattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		name := matches[1]
		version := matches[3] // exact version without ^ or ~

		packages = append(packages, DetectedPackage{
			Name:    name,
			Version: version,
		})
	}
	return packages
}
