// Package agentskills installs the Beacon Agent Skills (agent-skills/skills in the repository) into
// the user-level skill directories agent runtimes read, so every project on the machine sees them.
//
// The embedded copy under skills/ is kept byte-identical to agent-skills/skills by a test, the
// same arrangement the dashboard uses for the Lenses spec.
//
// A skill directory Beacon wrote carries a marker file next to SKILL.md. Beacon replaces or removes
// only directories that carry it, so a skill a person wrote or installed under the same name is
// never touched.
package agentskills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed skills
var embedded embed.FS

// MarkerFile is the file in a skill directory that says Beacon wrote it.
const MarkerFile = ".beacon-managed"

const markerContent = "beacon-managed-agent-skill:v1\n"

// Action is what happened to one skill in one directory.
type Action string

const (
	ActionAdd     Action = "add"     // the skill was written
	ActionUpdate  Action = "update"  // Beacon's earlier copy was replaced
	ActionPresent Action = "present" // Beacon's copy is already current
	ActionSkip    Action = "skip"    // a skill by that name exists that Beacon did not write
	ActionRemove  Action = "remove"  // uninstall removed Beacon's copy
)

// Result is one skill in one root.
type Result struct {
	Skill  string
	Root   string
	Path   string
	Action Action
	Err    error
}

// Names lists the embedded skills.
func Names() []string {
	entries, _ := fs.ReadDir(embedded, "skills")
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Roots are the user-level skill directories Beacon installs into: ~/.agents/skills, the
// cross-runtime location (Codex CLI, Gemini CLI, OpenCode, Pi and others read it), and Claude
// Code's own ~/.claude/skills when Claude Code is set up on this machine. Claude Code does not
// read ~/.agents/skills.
func Roots(home string) []string {
	roots := []string{filepath.Join(home, ".agents", "skills")}
	claudeDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if claudeDir == "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		roots = append(roots, filepath.Join(claudeDir, "skills"))
	}
	return roots
}

// Install writes every embedded skill into every root. Each skill is independent: one that fails
// is reported on its result and the rest still run.
func Install(roots []string) []Result {
	return apply(roots, false)
}

// Refresh brings the copies Beacon already wrote up to date with this binary, and writes nothing
// new. Install and repair run it, so an upgraded Beacon never leaves skills naming commands it no
// longer has.
func Refresh(roots []string) []Result {
	return apply(roots, true)
}

func apply(roots []string, onlyManaged bool) []Result {
	var out []Result
	for _, root := range roots {
		for _, name := range Names() {
			dir := filepath.Join(root, name)
			res := Result{Skill: name, Root: root, Path: dir}
			managed, exists, err := inspect(dir)
			switch {
			case err != nil:
				res.Action, res.Err = ActionSkip, err
			case exists && !managed:
				res.Action = ActionSkip
			case !exists && onlyManaged:
				continue
			case exists && current(dir, name):
				res.Action = ActionPresent
			default:
				res.Action = ActionAdd
				if exists {
					res.Action = ActionUpdate
				}
				res.Err = write(dir, name)
			}
			out = append(out, res)
		}
	}
	return out
}

// Uninstall removes the copies Beacon wrote, and nothing else.
func Uninstall(roots []string) []Result {
	var out []Result
	for _, root := range roots {
		for _, name := range Names() {
			dir := filepath.Join(root, name)
			managed, exists, err := inspect(dir)
			if err != nil {
				out = append(out, Result{Skill: name, Root: root, Path: dir, Action: ActionSkip, Err: err})
				continue
			}
			if !exists || !managed {
				continue
			}
			res := Result{Skill: name, Root: root, Path: dir, Action: ActionRemove}
			res.Err = os.RemoveAll(dir)
			out = append(out, res)
		}
	}
	return out
}

func inspect(dir string) (managed, exists bool, err error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.IsDir() {
		// A symlink or a file: something the person set up. Never followed or replaced.
		return false, true, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return false, true, err
	}
	return string(data) == markerContent, true, nil
}

// files lists one embedded skill's files, as slash paths relative to the skill directory.
func files(name string) ([]string, error) {
	var out []string
	base := path.Join("skills", name)
	err := fs.WalkDir(embedded, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, strings.TrimPrefix(p, base+"/"))
		}
		return nil
	})
	return out, err
}

// current reports whether dir holds exactly the embedded skill and the marker.
func current(dir, name string) bool {
	want, err := files(name)
	if err != nil {
		return false
	}
	count := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil || count != len(want)+1 {
		return false
	}
	for _, rel := range want {
		have, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return false
		}
		data, _ := embedded.ReadFile(path.Join("skills", name, rel))
		if string(have) != string(data) {
			return false
		}
	}
	return true
}

// write builds the skill in a temporary sibling directory and swaps it into place, so a runtime
// never reads half a skill and a failure leaves the previous copy intact.
func write(dir, name string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+name+".beacon-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	rels, err := files(name)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		data, err := embedded.ReadFile(path.Join("skills", name, rel))
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, MarkerFile), []byte(markerContent), 0o644); err != nil {
		return err
	}
	var old string
	if _, err := os.Lstat(dir); err == nil {
		old = tmp + ".old"
		if err := os.Rename(dir, old); err != nil {
			return fmt.Errorf("could not replace %s: %w", dir, err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if old != "" {
			_ = os.Rename(old, dir)
		}
		return err
	}
	if old != "" {
		return os.RemoveAll(old)
	}
	return nil
}
