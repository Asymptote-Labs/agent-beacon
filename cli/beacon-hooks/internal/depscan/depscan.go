package depscan

import (
	"path/filepath"
	"strings"
)

// DetectedPackage represents a package found in a dependency file diff.
type DetectedPackage struct {
	Name      string
	Version   string
	Ecosystem string
	FilePath  string
}

// dependencyFileInfo maps filename patterns to their ecosystem.
type dependencyFileInfo struct {
	ecosystem string
}

// knownDepFiles maps exact filenames to ecosystem info.
var knownDepFiles = map[string]dependencyFileInfo{
	"requirements.txt": {ecosystem: "PyPI"},
	"setup.py":         {ecosystem: "PyPI"},
	"Pipfile":          {ecosystem: "PyPI"},
	"pyproject.toml":   {ecosystem: "PyPI"},
	"package.json":     {ecosystem: "npm"},
}

// IsDependencyFile checks if a file path is a known dependency file.
func IsDependencyFile(filePath string) bool {
	base := filepath.Base(filePath)

	// Exact match
	if _, ok := knownDepFiles[base]; ok {
		return true
	}

	// requirements-*.txt pattern
	if strings.HasPrefix(base, "requirements-") && strings.HasSuffix(base, ".txt") {
		return true
	}

	// requirements/*.txt pattern (file inside a requirements directory)
	dir := filepath.Base(filepath.Dir(filePath))
	if dir == "requirements" && strings.HasSuffix(base, ".txt") {
		return true
	}

	return false
}

// getEcosystem returns the ecosystem for a dependency file path.
func getEcosystem(filePath string) string {
	base := filepath.Base(filePath)

	if info, ok := knownDepFiles[base]; ok {
		return info.ecosystem
	}

	// requirements-*.txt or requirements/*.txt
	if strings.HasSuffix(base, ".txt") {
		if strings.HasPrefix(base, "requirements-") {
			return "PyPI"
		}
		dir := filepath.Base(filepath.Dir(filePath))
		if dir == "requirements" {
			return "PyPI"
		}
	}

	return ""
}

// ParseDiffForPackages extracts added packages from a unified diff string for the given file.
func ParseDiffForPackages(diffStr, filePath string) []DetectedPackage {
	ecosystem := getEcosystem(filePath)
	if ecosystem == "" {
		return nil
	}

	// Extract added lines from the diff (lines starting with "+", excluding "+++")
	addedLines := extractAddedLines(diffStr)
	if len(addedLines) == 0 {
		return nil
	}

	var packages []DetectedPackage
	base := filepath.Base(filePath)

	switch {
	case ecosystem == "PyPI" && (base == "requirements.txt" ||
		strings.HasPrefix(base, "requirements-") ||
		filepath.Base(filepath.Dir(filePath)) == "requirements"):
		packages = ParseRequirementsTxt(addedLines)
	case ecosystem == "PyPI" && base == "pyproject.toml":
		packages = ParsePyprojectToml(addedLines)
	case ecosystem == "PyPI" && base == "setup.py":
		packages = ParsePyprojectToml(addedLines) // setup.py uses quoted format like pyproject.toml
	case ecosystem == "PyPI" && base == "Pipfile":
		packages = ParseRequirementsTxt(addedLines)
	case ecosystem == "npm" && base == "package.json":
		packages = ParsePackageJSON(addedLines)
	}

	// Set ecosystem and file path on all packages
	for i := range packages {
		packages[i].Ecosystem = ecosystem
		packages[i].FilePath = filePath
	}

	return packages
}

// extractAddedLines returns lines that were added in a unified diff (start with "+", excluding "+++").
func extractAddedLines(diffStr string) []string {
	var added []string
	for _, line := range strings.Split(diffStr, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			// Remove the leading "+"
			added = append(added, line[1:])
		}
	}
	return added
}
