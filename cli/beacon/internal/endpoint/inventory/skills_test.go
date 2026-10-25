package inventory

import (
	"path/filepath"
	"testing"
)

func TestSkillScanKeepsSkillsNamedLikeBuildDirectories(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	names := []string{"build", "dist", "vendor", "tmp", "temp"}
	for _, name := range names {
		writeFile(t, filepath.Join(home, ".claude", "skills", name, "SKILL.md"), "# "+name)
	}

	result := Scan(Options{
		HomeDir:    home,
		WorkingDir: work,
		Runtimes:   []string{"claude_code"},
		Now:        fixedNow,
	})

	for _, name := range names {
		if findSkill(result.Skills, "claude_code", name, filepath.Join(home, ".claude", "skills", name, "SKILL.md")) == nil {
			t.Errorf("Claude Code skill %q not found", name)
		}
	}
}
