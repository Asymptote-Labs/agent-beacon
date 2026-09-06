package main

import (
	"os"
	"path"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"gopkg.in/yaml.v3"
)

// brewConfig is the slice of .goreleaser.yaml the Homebrew formula is generated from.
type brewConfig struct {
	Brews []struct {
		Name         string `yaml:"name"`
		Dependencies []struct {
			Name string `yaml:"name"`
			OS   string `yaml:"os"`
		} `yaml:"dependencies"`
	} `yaml:"brews"`
}

func loadBrewConfig(t *testing.T) brewConfig {
	t.Helper()
	raw, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	var config brewConfig
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatalf("parse .goreleaser.yaml: %v", err)
	}
	if len(config.Brews) == 0 {
		t.Fatal("no brews entry in .goreleaser.yaml; the Homebrew formula is a release contract")
	}
	return config
}

// TestHomebrewFormulaNeverDependsOnAFormulaNamedVector pins the fix for a `brew upgrade
// beacon` that failed outright:
//
//	Error: vector is already installed from vectordotdev/brew!
//	Please `brew uninstall vector` first.
//
// Vector is not in homebrew-core, so the tap mirrors it, and the formula depends on that
// mirror so a Homebrew install can run `beacon endpoint connect` without a second step.
// The mirror must not be named `vector`: Homebrew allows exactly one keg by that name no
// matter which tap it came from, so on every machine that already had Vector from
// vectordotdev/brew -- the upstream tap, and the one vector.dev points at -- the dependency
// was unsatisfiable and took the whole beacon install down with it. No spelling of a
// `vector` dependency can be satisfied by both taps; only a different formula name can.
func TestHomebrewFormulaNeverDependsOnAFormulaNamedVector(t *testing.T) {
	for _, brew := range loadBrewConfig(t).Brews {
		for _, dep := range brew.Dependencies {
			// A tap-qualified dependency is owner/tap/formula, so the formula name is the
			// last path element; a bare name has no separator and is its own last element.
			if strings.EqualFold(path.Base(dep.Name), "vector") {
				t.Errorf("brew %q depends on %q: Homebrew allows one keg named \"vector\", so this "+
					"breaks installs for everyone who already has Vector from vectordotdev/brew. "+
					"Depend on the tap's %q mirror instead.", brew.Name, dep.Name, asymptote.TapVectorFormula)
			}
		}
	}
}

// TestHomebrewFormulaDependsOnTheVectorMirrorFindVectorLooksFor keeps the two halves of the
// one-step install in sync. The formula pulls in the tap's Vector mirror, and FindVector has
// to know the keg name to build the path it lives at -- Homebrew does not link libexec, so
// nothing else finds it. Renaming the formula on either side alone leaves `brew install
// beacon` installing a Vector that `beacon endpoint connect` cannot see.
//
// The dependency is macOS-only because the mirror only carries Vector's macOS tarballs.
// Dropping that guard would break Homebrew-on-Linux installs of Beacon outright, which is
// how the tap's macOS-only vector.rb behaved before the rename.
func TestHomebrewFormulaDependsOnTheVectorMirrorFindVectorLooksFor(t *testing.T) {
	want := "asymptote-labs/tap/" + asymptote.TapVectorFormula
	for _, brew := range loadBrewConfig(t).Brews {
		if brew.Name != "beacon" {
			continue
		}
		var found bool
		for _, dep := range brew.Dependencies {
			if dep.Name != want {
				continue
			}
			found = true
			if dep.OS != "mac" {
				t.Errorf("brew %q depends on %q with os %q, want %q: the mirror carries only "+
					"Vector's macOS tarballs, so an unguarded dependency breaks Homebrew-on-Linux "+
					"installs of Beacon", brew.Name, dep.Name, dep.OS, "mac")
			}
		}
		if !found {
			t.Errorf("brew %q does not depend on %q; FindVector looks for that keg by name, so "+
				"`beacon endpoint connect` would not find a Vector installed under another one",
				brew.Name, want)
		}
		return
	}
	t.Fatal(`no brews entry named "beacon" in .goreleaser.yaml`)
}
