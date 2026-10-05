package agentskills

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

const publishedSkills = "../../../../agent-skills/skills"

func normalize(data []byte) string {
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// The embedded copy is what onboarding installs; the published copy is what marketplaces ship.
// They must be the same skills. Edit agent-skills/skills and copy it here.
func TestEmbeddedSkillsMatchPublishedSkills(t *testing.T) {
	published := map[string]string{}
	err := filepath.WalkDir(publishedSkills, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(publishedSkills, p)
		data, err := os.ReadFile(p)
		published[filepath.ToSlash(rel)] = normalize(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	embeddedFiles := map[string]string{}
	err = fs.WalkDir(embedded, "skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := embedded.ReadFile(p)
		embeddedFiles[strings.TrimPrefix(p, "skills/")] = normalize(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) == 0 {
		t.Fatal("no published skills found")
	}
	for rel, want := range published {
		if got, ok := embeddedFiles[rel]; !ok {
			t.Errorf("%s is published but not embedded: copy agent-skills/skills into cli/beacon/internal/agentskills/skills", rel)
		} else if got != want {
			t.Errorf("%s differs from the published copy: copy agent-skills/skills into cli/beacon/internal/agentskills/skills", rel)
		}
	}
	for rel := range embeddedFiles {
		if _, ok := published[rel]; !ok {
			t.Errorf("%s is embedded but no longer published: remove it", rel)
		}
	}
}

func TestInstallRefreshUninstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	results := Install([]string{root})
	if len(results) != len(Names()) {
		t.Fatalf("results = %d, want %d", len(results), len(Names()))
	}
	for _, r := range results {
		if r.Err != nil || r.Action != ActionAdd {
			t.Fatalf("%s: action=%s err=%v", r.Skill, r.Action, r.Err)
		}
		data, err := os.ReadFile(filepath.Join(r.Path, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := embedded.ReadFile(path.Join("skills", r.Skill, "SKILL.md"))
		if string(data) != string(want) {
			t.Fatalf("%s: SKILL.md differs from the embedded copy", r.Skill)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "beacon-memory-distill", "references", "lesson-quality.md")); err != nil {
		t.Fatalf("nested reference not installed: %v", err)
	}

	for _, r := range Install([]string{root}) {
		if r.Action != ActionPresent {
			t.Fatalf("%s: second install action = %s", r.Skill, r.Action)
		}
	}

	// A stale copy Beacon wrote is brought up to date by Refresh.
	stale := filepath.Join(root, "beacon-memory-recall", "SKILL.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed := Refresh([]string{root})
	updated := 0
	for _, r := range refreshed {
		if r.Action == ActionUpdate {
			updated++
		}
	}
	if updated != 1 {
		t.Fatalf("refresh updated %d skills, want 1: %+v", updated, refreshed)
	}
	if data, _ := os.ReadFile(stale); string(data) == "old" {
		t.Fatal("refresh left the stale copy")
	}

	for _, r := range Uninstall([]string{root}) {
		if r.Err != nil || r.Action != ActionRemove {
			t.Fatalf("%s: action=%s err=%v", r.Skill, r.Action, r.Err)
		}
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("uninstall left %d entries", len(entries))
	}
}

func TestNeverTouchesASkillBeaconDidNotWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	mine := filepath.Join(root, "beacon-memory-recall")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range Install([]string{root}) {
		if r.Skill == "beacon-memory-recall" && r.Action != ActionSkip {
			t.Fatalf("action = %s, want skip", r.Action)
		}
	}
	Uninstall([]string{root})
	if data, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(data) != "mine" {
		t.Fatal("a skill Beacon did not write was changed")
	}
}

func TestRefreshWritesNothingNew(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	if results := Refresh([]string{root}); len(results) != 0 {
		t.Fatalf("refresh on an empty root = %+v", results)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("refresh created %s", root)
	}
}

func TestRootsAddClaudeOnlyWhenClaudeIsSetUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if roots := Roots(home); len(roots) != 1 || roots[0] != filepath.Join(home, ".agents", "skills") {
		t.Fatalf("roots = %v", roots)
	}
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if roots := Roots(home); len(roots) != 2 || roots[1] != filepath.Join(home, ".claude", "skills") {
		t.Fatalf("roots = %v", roots)
	}
}
