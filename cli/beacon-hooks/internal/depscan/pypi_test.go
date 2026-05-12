package depscan

import (
	"testing"
)

func TestParseRequirementsTxt(t *testing.T) {
	lines := []string{
		"flask==2.3.2",
		"jinja2==3.1.4",
		"requests>=2.31.0",
		"Django~=4.2.0",
		"# comment line",
		"",
		"-r base.txt",
		"  gunicorn==21.2.0  # with comment",
		"numpy<=1.26.0 ; python_version >= '3.9'",
	}

	packages := ParseRequirementsTxt(lines)

	expected := []struct {
		name, version string
	}{
		{"flask", "2.3.2"},
		{"jinja2", "3.1.4"},
		{"requests", "2.31.0"},
		{"django", "4.2.0"},
		{"gunicorn", "21.2.0"},
		{"numpy", "1.26.0"},
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

func TestParseRequirementsTxt_PipfileFormat(t *testing.T) {
	lines := []string{
		`flask = "==2.3.2"`,
		`requests = "*"`,
	}

	packages := ParseRequirementsTxt(lines)

	if len(packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(packages))
	}
	if packages[0].Name != "flask" || packages[0].Version != "2.3.2" {
		t.Errorf("got %s@%s, want flask@2.3.2", packages[0].Name, packages[0].Version)
	}
}

func TestParsePyprojectToml(t *testing.T) {
	lines := []string{
		`    "django>=4.2.0",`,
		`    "celery==5.3.1",`,
		`    "redis>=4.0.0",`,
		`[tool.ruff]`,
		`line-length = 88`,
	}

	packages := ParsePyprojectToml(lines)

	if len(packages) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(packages))
	}

	expected := []struct {
		name, version string
	}{
		{"django", "4.2.0"},
		{"celery", "5.3.1"},
		{"redis", "4.0.0"},
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

func TestNormalizePackageName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Flask", "flask"},
		{"Jinja2", "jinja2"},
		{"my-package", "my_package"},
		{"some.pkg", "some_pkg"},
		{"My-Cool.Package", "my_cool_package"},
	}

	for _, tt := range tests {
		got := normalizePackageName(tt.input)
		if got != tt.want {
			t.Errorf("normalizePackageName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
