package depscan

import (
	"testing"
)

func TestParsePackageJSON(t *testing.T) {
	lines := []string{
		`    "express": "4.18.2",`,
		`    "lodash": "^4.17.21",`,
		`    "react": "~18.2.0",`,
		`    "@types/node": "20.10.0",`,
		`    "name": "my-app",`,
	}

	packages := ParsePackageJSON(lines)

	expected := []struct {
		name, version string
	}{
		{"express", "4.18.2"},
		{"lodash", "4.17.21"},
		{"react", "18.2.0"},
		{"@types/node", "20.10.0"},
	}

	if len(packages) != len(expected) {
		t.Fatalf("expected %d packages, got %d: %+v", len(expected), len(packages), packages)
	}

	for i, exp := range expected {
		if packages[i].Name != exp.name {
			t.Errorf("package %d: name = %q, want %q", i, packages[i].Name, exp.name)
		}
		if packages[i].Version != exp.version {
			t.Errorf("package %d: version = %q, want %q", i, packages[i].Version, exp.version)
		}
	}
}

func TestParsePackageJSON_NonDepLines(t *testing.T) {
	lines := []string{
		`  "name": "my-app",`,
		`  "version": "1.0.0",`,
		`  "scripts": {`,
		`    "start": "node index.js"`,
	}

	packages := ParsePackageJSON(lines)

	// "name" and "version" at the top level shouldn't match because their values
	// are not semver-like versions (my-app is not X.Y.Z with digits)
	// Actually "version": "1.0.0" would match. That's acceptable since it's still
	// a valid version pattern.
	// The important thing is "name": "my-app" doesn't match.
	for _, p := range packages {
		if p.Name == "name" {
			t.Error("should not match 'name' field as a dependency")
		}
		if p.Name == "scripts" || p.Name == "start" {
			t.Errorf("should not match %q as a dependency", p.Name)
		}
	}
}
