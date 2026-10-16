// Package shareauto holds the auto-share setting: whether the hook adapter records one
// session.share_requested event per session, which Beacon Cloud forwarding ships to beacon.sh and
// beacon.sh turns into a shared trace that it keeps refreshing as more of the session arrives.
//
// The hooks never publish anything themselves and never touch the network: they only write that
// local event, and only when the person turned the setting on with `beacon share auto enable`.
//
// Two files hold the setting:
//
//   - The user file, ~/.beacon/endpoint/share.json (0600 under 0700), written only by the CLI. It
//     has the user-wide switch and the projects the user opted in or out of, by absolute path.
//   - A project marker, <project>/.beacon/share.json, which `--project` writes so a repository can
//     record its choice. A marker can turn auto-share off for its project on its own. It can turn it
//     on only for someone whose user file trusts that project: a marker arrives with a clone, and a
//     repository must never be able to publish the sessions of whoever opens it. The visibility of
//     a trusted project always comes from the user file, never from the marker.
package shareauto

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// FileName is the user file's name under the endpoint base directory, and the marker's name
	// under a project's .beacon directory.
	FileName = "share.json"
	// MarkerDir is the project directory that holds the marker.
	MarkerDir = ".beacon"
	// PathEnv overrides where the user file lives (tests, unusual homes).
	PathEnv = "BEACON_SHARE_SETTINGS_FILE"
	// SchemaVersion is written into both files.
	SchemaVersion = "beacon.share_settings.v1"

	// maxWalk bounds how many directories are searched above a working directory for a project.
	maxWalk = 64
)

// Auto is one auto-share switch and the visibility it shares with ("" means the server default,
// direct). In the user file the switch has three states (State): unset, on, or off; off is a kill
// switch over every project. Enabled is kept equal to State == on, so a file written by an older
// Beacon (enabled only) still reads: enabled true is on, false is unset. A marker uses Enabled only.
type Auto struct {
	Enabled    bool   `json:"enabled"`
	State      string `json:"state,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// The user-wide switch's states.
const (
	GlobalUnset = ""
	GlobalOn    = "on"
	GlobalOff   = "off"
)

// Project is the user's choice for one project directory.
type Project struct {
	Path       string `json:"path"`
	Enabled    bool   `json:"enabled"`
	Visibility string `json:"visibility,omitempty"`
}

// Settings is the user file.
type Settings struct {
	SchemaVersion string    `json:"schema_version"`
	Auto          Auto      `json:"auto"`
	Projects      []Project `json:"projects,omitempty"`
}

// Marker is a project's .beacon/share.json.
type Marker struct {
	SchemaVersion string `json:"schema_version"`
	Auto          Auto   `json:"auto"`
}

// Decision is what applies to one working directory.
type Decision struct {
	Enabled    bool
	Visibility string
	// Source is "user" (the user-wide switch, on, or off as the kill switch), "project" (the user's
	// entry for a project), "marker" (a project marker turned it off) or "" when nothing is
	// configured.
	Source string
	// Project is the project directory the decision came from, when it came from one.
	Project string
	// Overridden is the project whose enabled user entry this decision overrides: by the user-wide
	// off switch (Source "user") or by the repository's opt-out marker (Source "marker").
	Overridden string
	// UntrustedMarker is a marker that asks for auto-share in a project the user has not opted in.
	UntrustedMarker string
}

// Global is the user-wide switch's state: GlobalOn, GlobalOff or GlobalUnset.
func (s Settings) Global() string {
	switch s.Auto.State {
	case GlobalOn, GlobalOff:
		return s.Auto.State
	}
	if s.Auto.Enabled {
		return GlobalOn
	}
	return GlobalUnset
}

// SetGlobal sets the user-wide switch, keeping Enabled in step for older readers.
func (s *Settings) SetGlobal(state string) {
	s.Auto.State = state
	s.Auto.Enabled = state == GlobalOn
}

// DefaultPath is BEACON_SHARE_SETTINGS_FILE, or ~/.beacon/endpoint/share.json.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv(PathEnv)); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".beacon", "endpoint", FileName)
}

// MarkerPath is dir's project marker.
func MarkerPath(dir string) string { return filepath.Join(dir, MarkerDir, FileName) }

// Load reads the user file. A missing file is an empty Settings and no error.
func Load(path string) (Settings, error) {
	s := Settings{SchemaVersion: SchemaVersion}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{SchemaVersion: SchemaVersion}, err
	}
	s.SchemaVersion = SchemaVersion
	return s, nil
}

// LoadMarker reads dir's project marker; ok is false when there is none or it cannot be read.
func LoadMarker(dir string) (Marker, bool) {
	data, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		return Marker{}, false
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, false
	}
	return m, true
}

// anyEnabled reports whether anything in s could turn auto-share on, so the hook path can skip
// the directory walk entirely when the feature is not in use.
func (s Settings) anyEnabled() bool {
	switch s.Global() {
	case GlobalOff:
		return false
	case GlobalOn:
		return true
	}
	for _, p := range s.Projects {
		if p.Enabled {
			return true
		}
	}
	return false
}

// Project returns the user's entry for dir. Paths are compared the way the filesystem names them
// (see pathKey), so an entry written with a symlink, another case on Windows, or a short 8.3 name
// still matches.
func (s Settings) Project(dir string) (Project, bool) {
	key := pathKey(dir)
	for _, p := range s.Projects {
		if pathKey(p.Path) == key {
			return p, true
		}
	}
	return Project{}, false
}

// Resolve decides whether auto-share applies in workDir. The user-wide switch turned off is a kill
// switch: nothing applies anywhere, and the project entry it overrides is reported. Otherwise the
// nearest enclosing directory that has a user entry or a marker decides, walking from workDir
// outward. At that directory a marker that says off wins over the user's entry (the repository
// opted out; the overridden entry is reported); otherwise the user's entry decides. A marker that
// asks for auto-share without a user entry is reported and otherwise ignored. A closer directory
// decides over a farther one, whichever kind each is: a user entry for a subdirectory enables it
// even when a parent's marker says off. With neither, the user-wide switch decides (on, or unset
// meaning off).
func Resolve(s Settings, workDir string) Decision {
	d := resolveProjects(s, workDir)
	if s.Global() == GlobalOff {
		off := Decision{Source: "user", UntrustedMarker: d.UntrustedMarker}
		if d.Enabled && d.Source == "project" {
			off.Overridden = d.Project
		}
		return off
	}
	return d
}

func resolveProjects(s Settings, workDir string) Decision {
	var d Decision
	dir := filepath.Clean(workDir)
	if workDir != "" && rooted(dir) {
		projects := make(map[string]Project, len(s.Projects))
		for _, p := range s.Projects {
			if k := pathKey(p.Path); k != "" {
				if _, dup := projects[k]; !dup {
					projects[k] = p
				}
			}
		}
		for i := 0; i < maxWalk; i++ {
			p, hasEntry := projects[pathKey(dir)]
			if m, ok := LoadMarker(dir); ok {
				if !m.Auto.Enabled {
					// The repository opted out, and that wins over the user's own entry for the
					// same directory: a committed opt-out must hold for everyone who opens it.
					off := Decision{Source: "marker", Project: dir, UntrustedMarker: d.UntrustedMarker}
					if hasEntry && p.Enabled {
						off.Overridden = dir
					}
					return off
				}
				if !hasEntry && d.UntrustedMarker == "" {
					d.UntrustedMarker = dir
				}
			}
			if hasEntry {
				return Decision{Enabled: p.Enabled, Visibility: p.Visibility, Source: "project", Project: dir, UntrustedMarker: d.UntrustedMarker}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if s.Global() == GlobalOn {
		d.Enabled, d.Visibility, d.Source = true, s.Auto.Visibility, "user"
	}
	return d
}

// SamePath reports whether a and b name the same directory, compared as Project and Resolve
// compare them.
func SamePath(a, b string) bool {
	ka := pathKey(a)
	return ka != "" && ka == pathKey(b)
}

// rooted is true for a path that names a directory from the top of a filesystem. On Windows that
// includes a path rooted on the current drive (`\work\project`, `/work/project`), which a runtime
// may report without a drive letter.
func rooted(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	return runtime.GOOS == "windows" && (strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "/"))
}

// pathKey is the form two paths are compared in: cleaned, with symlinks resolved when the path
// exists (which on Windows also expands 8.3 short names and normalizes the drive letter), and on
// Windows case-folded, because its filesystems ignore case.
func pathKey(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// ForHook is Resolve for the hook path: it reads the user file and returns a disabled decision at
// the cost of one failed read when nothing could enable auto-share.
func ForHook(path, workDir string) Decision {
	s, err := Load(path)
	if err != nil || !s.anyEnabled() {
		return Decision{}
	}
	return Resolve(s, workDir)
}

// Save writes the user file atomically, 0600 under a 0700 directory.
func Save(path string, s Settings) error {
	s.SchemaVersion = SchemaVersion
	return writeJSON(path, s, 0o700, 0o600)
}

// SaveMarker writes dir's project marker. It is meant to be committed, so it is an ordinary file.
func SaveMarker(dir string, m Marker) error {
	m.SchemaVersion = SchemaVersion
	return writeJSON(MarkerPath(dir), m, 0o755, 0o644)
}

func writeJSON(path string, v any, dirMode, fileMode os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+FileName+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
