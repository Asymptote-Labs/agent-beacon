package inventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const maxSkillsPerRoot = 200

type Skill struct {
	Runtime          string           `json:"runtime"`
	SkillName        string           `json:"skill_name,omitempty"`
	SkillNameHash    string           `json:"skill_name_hash,omitempty"`
	RootPath         string           `json:"root_path,omitempty"`
	RootPathHash     string           `json:"root_path_hash,omitempty"`
	SourceScope      string           `json:"source_scope"`
	ManifestPath     string           `json:"manifest_path,omitempty"`
	ManifestPathHash string           `json:"manifest_path_hash,omitempty"`
	Exists           bool             `json:"exists"`
	Readable         bool             `json:"readable"`
	Reason           string           `json:"reason,omitempty"`
	ParserStatus     string           `json:"parser_status"`
	FileSHA256       string           `json:"file_sha256,omitempty"`
	ModifiedAt       string           `json:"modified_at,omitempty"`
	Redaction        string           `json:"redaction"`
	Content          *CapturedContent `json:"content,omitempty"`
	// IntelKey is the skill's Beacon Intel entity key, skill:<owner>/<repo>#<name>: the GitHub
	// repository the skills CLI (npx skills add) recorded installing it from in its lock file,
	// and the name it installed it under. Empty when nothing records one.
	IntelKey string `json:"intel_key,omitempty"`
}

type skillRoot struct {
	runtime string
	path    string
	scope   string
}

func scanSkills(home, wd, redaction string, runtimes []string, co contentOptions) []Skill {
	var skills []Skill
	roots := skillRoots(home, wd)
	if wd == "" {
		kept := roots[:0]
		for _, root := range roots {
			if root.scope != ScopeProject {
				kept = append(kept, root)
			}
		}
		roots = kept
	}
	sources := newSkillSources(home)
	for _, root := range filterSkillRoots(roots, runtimes) {
		skills = append(skills, inspectSkillRoot(root, redaction, co, sources)...)
	}
	return dedupeSkills(skills)
}

func filterSkillRoots(items []skillRoot, runtimes []string) []skillRoot {
	allowed := runtimeSet(runtimes)
	if len(allowed) == 0 {
		return items
	}
	out := make([]skillRoot, 0, len(items))
	for _, item := range items {
		if allowed[item.runtime] {
			out = append(out, item)
		}
	}
	return out
}

func skillRoots(home, wd string) []skillRoot {
	items := []skillRoot{}
	if dir := claudesession.ConfigDir(home); dir != "" {
		items = append(items, skillRoot{runtime: "claude_code", path: filepath.Join(dir, "skills"), scope: ScopeUser})
	}
	items = append(items, []skillRoot{
		{runtime: "claude_code", path: filepath.Join(wd, ".claude", "skills"), scope: ScopeProject},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "skills"), scope: ScopeUser},
		{runtime: "cursor", path: filepath.Join(wd, ".cursor", "skills"), scope: ScopeProject},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "skills-cursor"), scope: ScopeUser},
		{runtime: "cursor", path: filepath.Join(wd, ".cursor", "skills-cursor"), scope: ScopeProject},
		{runtime: "agent_skills", path: filepath.Join(home, ".agents", "skills"), scope: ScopeUser},
		{runtime: "agent_skills", path: filepath.Join(wd, ".agents", "skills"), scope: ScopeProject},
	}...)
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

func inspectSkillRoot(root skillRoot, redaction string, co contentOptions, sources *skillSources) []Skill {
	base := Skill{
		Runtime:      root.runtime,
		RootPath:     valueForPath(root.path, redaction),
		RootPathHash: hashString(root.path),
		SourceScope:  root.scope,
		ParserStatus: StatusNotFound,
		Redaction:    redaction,
	}
	info, err := statPath(root.path)
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
	entries, err := readDirPath(root.path)
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
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		manifestPath := filepath.Join(root.path, entry.Name(), "SKILL.md")
		if _, err := statPath(manifestPath); os.IsNotExist(err) {
			continue
		}
		skill := inspectSkillManifest(root, entry.Name(), manifestPath, redaction, co)
		if skill.Exists {
			skill.IntelKey = sources.intelKey(filepath.Join(root.path, entry.Name()))
		}
		skills = append(skills, skill)
	}
	if len(skills) == 0 {
		return nil
	}
	return skills
}

func inspectSkillManifest(root skillRoot, name, manifestPath, redaction string, co contentOptions) Skill {
	skill := Skill{
		Runtime:          root.runtime,
		SkillName:        valueForName(name, redaction),
		SkillNameHash:    asymptoteobserve.SkillNameHash(name),
		RootPath:         valueForPath(root.path, redaction),
		RootPathHash:     hashString(root.path),
		SourceScope:      root.scope,
		ManifestPath:     valueForPath(manifestPath, redaction),
		ManifestPathHash: hashString(manifestPath),
		ParserStatus:     StatusNotFound,
		Redaction:        redaction,
	}
	info, err := statPath(manifestPath)
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
	data, err := readFilePath(manifestPath)
	if err != nil {
		skill.ParserStatus = StatusUnreadable
		skill.Reason = errReason(err)
		return skill
	}
	skill.Readable = true
	skill.FileSHA256 = hashBytes(data)
	if co.include {
		skill.Content = captureContent(data, co.limit())
	}
	skill.ParserStatus = StatusOK
	return skill
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

// skillLockFile is where the skills CLI records what it installed into ~/.agents/skills.
const skillLockFile = ".skill-lock.json"

var githubRepo = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// skillSources is what the skills CLI recorded about the skills it installed into
// ~/.agents/skills: the GitHub repository of each, by name, in ~/.agents/.skill-lock.json.
//
// Only that lock counts. A lock beside any other skills directory (<repo>/.claude/.skill-lock.json
// for <repo>/.claude/skills, say) may be written by whoever wrote the skills, a cloned repository
// included, and could name a known artifact's repository for a skill of its own, so Beacon Intel's
// verdict for that artifact would be shown for it.
type skillSources struct {
	// dir is ~/.agents/skills with symlinks resolved, as a skill directory resolves.
	dir    string
	lock   string
	loaded bool
	repos  map[string]string
}

func newSkillSources(home string) *skillSources {
	agents := filepath.Join(home, ".agents")
	dir := filepath.Join(agents, "skills")
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return &skillSources{dir: dir, lock: filepath.Join(agents, skillLockFile)}
}

// intelKey is the Beacon Intel key of the skill in dir, when it is the skills CLI's copy in
// ~/.agents/skills and the CLI recorded the GitHub repository it installed it from. Runtimes'
// own skill directories usually link each skill to that copy, so the link is followed to it.
func (s *skillSources) intelKey(dir string) string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Dir(real) != s.dir {
		return ""
	}
	if !s.loaded {
		s.repos, s.loaded = readSkillLock(s.lock), true
	}
	name := filepath.Base(real)
	if repo := s.repos[name]; repo != "" {
		return "skill:" + repo + "#" + name
	}
	return ""
}

func readSkillLock(path string) map[string]string {
	data, err := readFilePath(path)
	if err != nil {
		return nil
	}
	var lock struct {
		Skills map[string]struct {
			Source     string `json:"source"`
			SourceType string `json:"sourceType"`
		} `json:"skills"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return nil
	}
	repos := map[string]string{}
	for name, entry := range lock.Skills {
		if entry.SourceType == "github" && githubRepo.MatchString(entry.Source) && !strings.Contains(entry.Source, "..") {
			repos[name] = entry.Source
		}
	}
	return repos
}
