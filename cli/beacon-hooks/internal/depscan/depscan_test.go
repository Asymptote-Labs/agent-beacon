package depscan

import (
	"testing"
)

func TestIsDependencyFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"requirements.txt", true},
		{"./requirements.txt", true},
		{"/app/requirements.txt", true},
		{"requirements-dev.txt", true},
		{"requirements-test.txt", true},
		{"requirements/base.txt", true},
		{"pyproject.toml", true},
		{"setup.py", true},
		{"Pipfile", true},
		{"package.json", true},
		{"/app/package.json", true},

		// Not dependency files
		{"app.py", false},
		{"main.go", false},
		{"index.js", false},
		{"README.txt", false},
		{"requirements.md", false},
		{"config.json", false},
		{"package-lock.json", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := IsDependencyFile(tt.path)
			if got != tt.want {
				t.Errorf("IsDependencyFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestParseDiffForPackages_RequirementsTxt(t *testing.T) {
	diff := `--- a/requirements.txt
+++ b/requirements.txt
@@ -0,0 +1,4 @@
+flask==2.3.2
+jinja2==3.1.4
+requests>=2.31.0
+# this is a comment`

	packages := ParseDiffForPackages(diff, "requirements.txt")

	if len(packages) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(packages))
	}

	expected := []struct {
		name, version, ecosystem string
	}{
		{"flask", "2.3.2", "PyPI"},
		{"jinja2", "3.1.4", "PyPI"},
		{"requests", "2.31.0", "PyPI"},
	}

	for i, exp := range expected {
		if packages[i].Name != exp.name {
			t.Errorf("package %d: name = %q, want %q", i, packages[i].Name, exp.name)
		}
		if packages[i].Version != exp.version {
			t.Errorf("package %d: version = %q, want %q", i, packages[i].Version, exp.version)
		}
		if packages[i].Ecosystem != exp.ecosystem {
			t.Errorf("package %d: ecosystem = %q, want %q", i, packages[i].Ecosystem, exp.ecosystem)
		}
	}
}

func TestParseDiffForPackages_PackageJSON(t *testing.T) {
	diff := `--- a/package.json
+++ b/package.json
@@ -5,0 +6,3 @@
+    "express": "4.18.2",
+    "lodash": "^4.17.21",
+    "react": "~18.2.0"`

	packages := ParseDiffForPackages(diff, "package.json")

	if len(packages) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(packages))
	}

	expected := []struct {
		name, version string
	}{
		{"express", "4.18.2"},
		{"lodash", "4.17.21"},
		{"react", "18.2.0"},
	}

	for i, exp := range expected {
		if packages[i].Name != exp.name {
			t.Errorf("package %d: name = %q, want %q", i, packages[i].Name, exp.name)
		}
		if packages[i].Version != exp.version {
			t.Errorf("package %d: version = %q, want %q", i, packages[i].Version, exp.version)
		}
		if packages[i].Ecosystem != "npm" {
			t.Errorf("package %d: ecosystem = %q, want npm", i, packages[i].Ecosystem)
		}
	}
}

func TestParseDiffForPackages_PyprojectToml(t *testing.T) {
	diff := `--- a/pyproject.toml
+++ b/pyproject.toml
@@ -10,0 +11,3 @@
+    "django>=4.2.0",
+    "celery==5.3.1",`

	packages := ParseDiffForPackages(diff, "pyproject.toml")

	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(packages))
	}

	if packages[0].Name != "django" || packages[0].Version != "4.2.0" {
		t.Errorf("package 0: got %s@%s, want django@4.2.0", packages[0].Name, packages[0].Version)
	}
	if packages[1].Name != "celery" || packages[1].Version != "5.3.1" {
		t.Errorf("package 1: got %s@%s, want celery@5.3.1", packages[1].Name, packages[1].Version)
	}
}

func TestParseDiffForPackages_NonDepFile(t *testing.T) {
	diff := `--- a/app.py
+++ b/app.py
@@ -0,0 +1 @@
+import flask`

	packages := ParseDiffForPackages(diff, "app.py")
	if len(packages) != 0 {
		t.Errorf("expected 0 packages for non-dep file, got %d", len(packages))
	}
}

func TestParseDiffForPackages_NoAddedLines(t *testing.T) {
	diff := `--- a/requirements.txt
+++ b/requirements.txt
@@ -1 +1 @@
-flask==2.3.1`

	packages := ParseDiffForPackages(diff, "requirements.txt")
	if len(packages) != 0 {
		t.Errorf("expected 0 packages when no added lines, got %d", len(packages))
	}
}

func TestParseDiffForPackages_RequirementsDevTxt(t *testing.T) {
	diff := `--- a/requirements-dev.txt
+++ b/requirements-dev.txt
@@ -0,0 +1,2 @@
+pytest==7.4.0
+black==23.7.0`

	packages := ParseDiffForPackages(diff, "requirements-dev.txt")

	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(packages))
	}
	if packages[0].Ecosystem != "PyPI" {
		t.Errorf("ecosystem = %q, want PyPI", packages[0].Ecosystem)
	}
}

func TestParseDiffForPackages_RequirementsSubdir(t *testing.T) {
	diff := `--- a/requirements/base.txt
+++ b/requirements/base.txt
@@ -0,0 +1 @@
+django==5.1.7`

	packages := ParseDiffForPackages(diff, "requirements/base.txt")

	if len(packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(packages))
	}
	if packages[0].Name != "django" {
		t.Errorf("name = %q, want django", packages[0].Name)
	}
	if packages[0].Ecosystem != "PyPI" {
		t.Errorf("ecosystem = %q, want PyPI", packages[0].Ecosystem)
	}
}

func TestParseDiffForPackages_SetupPy(t *testing.T) {
	diff := `--- a/setup.py
+++ b/setup.py
@@ -10,0 +11,2 @@
+        "flask==2.3.0",
+        "requests>=2.31.0",`

	packages := ParseDiffForPackages(diff, "setup.py")

	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(packages))
	}
}

func TestGetEcosystem(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"requirements.txt", "PyPI"},
		{"requirements-dev.txt", "PyPI"},
		{"requirements/base.txt", "PyPI"},
		{"pyproject.toml", "PyPI"},
		{"setup.py", "PyPI"},
		{"Pipfile", "PyPI"},
		{"package.json", "npm"},
		{"app.py", ""},
		{"unknown.txt", ""},
	}

	for _, tt := range tests {
		got := getEcosystem(tt.path)
		if got != tt.want {
			t.Errorf("getEcosystem(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestExtractAddedLines(t *testing.T) {
	diff := `--- a/file
+++ b/file
@@ -1,2 +1,3 @@
 unchanged
-removed
+added1
+added2`

	lines := extractAddedLines(diff)
	if len(lines) != 2 {
		t.Fatalf("expected 2 added lines, got %d", len(lines))
	}
	if lines[0] != "added1" {
		t.Errorf("line 0 = %q, want %q", lines[0], "added1")
	}
	if lines[1] != "added2" {
		t.Errorf("line 1 = %q, want %q", lines[1], "added2")
	}
}
