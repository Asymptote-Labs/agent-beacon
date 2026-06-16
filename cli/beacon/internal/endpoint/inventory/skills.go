package inventory

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxSkillsPerRoot = 200

type Skill struct {
	Runtime          string `json:"runtime"`
	SkillName        string `json:"skill_name,omitempty"`
	SkillNameHash    string `json:"skill_name_hash,omitempty"`
	RootPath         string `json:"root_path,omitempty"`
	RootPathHash     string `json:"root_path_hash,omitempty"`
	SourceScope      string `json:"source_scope"`
	ManifestPath     string `json:"manifest_path,omitempty"`
	ManifestPathHash string `json:"manifest_path_hash,omitempty"`
	Exists           bool   `json:"exists"`
	Readable         bool   `json:"readable"`
	Reason           string `json:"reason,omitempty"`
	ParserStatus     string `json:"parser_status"`
	FileSHA256       string `json:"file_sha256,omitempty"`
	ModifiedAt       string `json:"modified_at,omitempty"`
	Redaction        string `json:"redaction"`
}

type skillRoot struct {
	runtime string
	path    string
	scope   string
}

func scanSkills(home, wd, redaction string) []Skill {
	var skills []Skill
	for _, root := range skillRoots(home, wd) {
		skills = append(skills, inspectSkillRoot(root, redaction)...)
	}
	return dedupeSkills(skills)
}

func skillRoots(home, wd string) []skillRoot {
	items := []skillRoot{
		{runtime: "claude_code", path: filepath.Join(home, ".claude", "skills"), scope: ScopeUser},
		{runtime: "claude_code", path: filepath.Join(wd, ".claude", "skills"), scope: ScopeProject},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "skills"), scope: ScopeUser},
		{runtime: "cursor", path: filepath.Join(wd, ".cursor", "skills"), scope: ScopeProject},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "skills-cursor"), scope: ScopeUser},
		{runtime: "cursor", path: filepath.Join(wd, ".cursor", "skills-cursor"), scope: ScopeProject},
		{runtime: "agent_skills", path: filepath.Join(home, ".agents", "skills"), scope: ScopeUser},
		{runtime: "agent_skills", path: filepath.Join(wd, ".agents", "skills"), scope: ScopeProject},
	}
	seen := map[string]bool{}
	out := make([]skillRoot, 0, len(items))
	for _, item := range items {
		key := item.runtime + "\x00" + item.path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func inspectSkillRoot(root skillRoot, redaction string) []Skill {
	base := Skill{
		Runtime:      root.runtime,
		RootPath:     valueForPath(root.path, redaction),
		RootPathHash: hashString(root.path),
		SourceScope:  root.scope,
		ParserStatus: StatusNotFound,
		Redaction:    redaction,
	}
	info, err := os.Stat(root.path)
	if err != nil {
		base.Reason = errReason(err)
		if !os.IsNotExist(err) {
			base.ParserStatus = StatusUnreadable
		}
		return []Skill{base}
	}
	base.Exists = true
	if !info.IsDir() {
		base.ParserStatus = StatusUnsupported
		base.Reason = "path is not a directory"
		return []Skill{base}
	}
	entries, err := os.ReadDir(root.path)
	if err != nil {
		base.ParserStatus = StatusUnreadable
		base.Reason = errReason(err)
		return []Skill{base}
	}
	var skills []Skill
	for _, entry := range entries {
		if len(skills) >= maxSkillsPerRoot {
			break
		}
		if !entry.IsDir() || skipSkillDir(entry.Name()) {
			continue
		}
		manifestPath := filepath.Join(root.path, entry.Name(), "SKILL.md")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}
		skills = append(skills, inspectSkillManifest(root, entry.Name(), manifestPath, redaction))
	}
	if len(skills) == 0 {
		return nil
	}
	return skills
}

func inspectSkillManifest(root skillRoot, name, manifestPath, redaction string) Skill {
	skill := Skill{
		Runtime:          root.runtime,
		SkillName:        valueForName(name, redaction),
		SkillNameHash:    hashString(name),
		RootPath:         valueForPath(root.path, redaction),
		RootPathHash:     hashString(root.path),
		SourceScope:      root.scope,
		ManifestPath:     valueForPath(manifestPath, redaction),
		ManifestPathHash: hashString(manifestPath),
		ParserStatus:     StatusNotFound,
		Redaction:        redaction,
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		skill.Reason = errReason(err)
		if !os.IsNotExist(err) {
			skill.ParserStatus = StatusUnreadable
		}
		return skill
	}
	skill.Exists = true
	skill.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
	if info.IsDir() {
		skill.ParserStatus = StatusUnsupported
		skill.Reason = "manifest path is a directory"
		return skill
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		skill.ParserStatus = StatusUnreadable
		skill.Reason = errReason(err)
		return skill
	}
	skill.Readable = true
	skill.FileSHA256 = hashBytes(data)
	skill.ParserStatus = StatusOK
	return skill
}

func skipSkillDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "tmp", "temp":
		return true
	default:
		return false
	}
}

func dedupeSkills(skills []Skill) []Skill {
	seen := map[string]bool{}
	out := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		key := skill.Runtime + "\x00" + skill.SourceScope + "\x00" + skill.RootPathHash + "\x00" + skill.SkillNameHash + "\x00" + skill.ManifestPathHash
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runtime == out[j].Runtime {
			if out[i].SourceScope == out[j].SourceScope {
				if out[i].RootPathHash == out[j].RootPathHash {
					return out[i].SkillNameHash < out[j].SkillNameHash
				}
				return out[i].RootPathHash < out[j].RootPathHash
			}
			return out[i].SourceScope < out[j].SourceScope
		}
		return out[i].Runtime < out[j].Runtime
	})
	return out
}
