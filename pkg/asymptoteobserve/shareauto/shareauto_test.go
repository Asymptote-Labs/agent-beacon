package shareauto

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if d := Resolve(Settings{}, sub); d.Enabled || d.Source != "" {
		t.Fatalf("empty = %+v", d)
	}
	user := Settings{Auto: Auto{Enabled: true, Visibility: "private"}}
	if d := Resolve(user, sub); !d.Enabled || d.Visibility != "private" || d.Source != "user" {
		t.Fatalf("user = %+v", d)
	}
	// A marker turning it off wins on its own.
	if err := SaveMarker(repo, Marker{Auto: Auto{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if d := Resolve(user, sub); d.Enabled || d.Source != "marker" || d.Project != repo {
		t.Fatalf("marker off = %+v", d)
	}
	// A marker asking for public auto-share does nothing without the user's trust.
	if err := SaveMarker(repo, Marker{Auto: Auto{Enabled: true, Visibility: "public"}}); err != nil {
		t.Fatal(err)
	}
	if d := Resolve(Settings{}, sub); d.Enabled || d.UntrustedMarker != repo {
		t.Fatalf("untrusted marker = %+v", d)
	}
	if d := Resolve(user, sub); d.Visibility != "private" || d.Source != "user" {
		t.Fatalf("untrusted marker overrode the user visibility: %+v", d)
	}
	// The user's project entry decides, with its own visibility.
	trusted := Settings{Projects: []Project{{Path: repo, Enabled: true, Visibility: "direct"}}}
	if d := Resolve(trusted, sub); !d.Enabled || d.Visibility != "direct" || d.Source != "project" {
		t.Fatalf("trusted = %+v", d)
	}
	optOut := Settings{Auto: Auto{Enabled: true}, Projects: []Project{{Path: repo, Enabled: false}}}
	if d := Resolve(optOut, sub); d.Enabled {
		t.Fatalf("project opt-out = %+v", d)
	}
	if d := Resolve(trusted, root); d.Enabled {
		t.Fatalf("outside the project = %+v", d)
	}
}

// A repository can put HOME in the hook environment. HookPath follows the account, so moving HOME
// (or USERPROFILE) does not move it, and a share.json planted under the forged home is never read.
func TestHookPathIgnoresForgedHome(t *testing.T) {
	before := HookPath()
	if before == "" {
		t.Skip("this account has no home directory")
	}
	forged := t.TempDir()
	t.Setenv("HOME", forged)
	t.Setenv("USERPROFILE", forged)
	if got := HookPath(); got != before || strings.HasPrefix(got, forged) {
		t.Fatalf("HookPath with a forged HOME = %q, want %q", got, before)
	}
}

func TestForHookAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint", FileName)
	if d := ForHook(path, "/x"); d.Enabled {
		t.Fatal("missing file enabled")
	}
	if err := Save(path, Settings{Auto: Auto{Enabled: true, Visibility: "direct"}}); err != nil {
		t.Fatal(err)
	}
	if d := ForHook(path, "/x"); !d.Enabled || d.Visibility != "direct" {
		t.Fatalf("ForHook = %+v", d)
	}
	if info, err := os.Stat(path); err == nil && os.PathSeparator == '/' && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := ForHook(path, "/x"); d.Enabled {
		t.Fatal("damaged file enabled")
	}
}

// Project entries match the directory however its path is spelled: through a symlink, with a
// trailing separator, or (on Windows) in another case or rooted on the current drive.
func TestResolveMatchesEquivalentPaths(t *testing.T) {
	real := t.TempDir()
	work := filepath.Join(real, "sub")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Settings{Auto: Auto{Enabled: true}, Projects: []Project{{Path: real + string(filepath.Separator), Enabled: false}}}
	if d := Resolve(s, work); d.Enabled || d.Source != "project" {
		t.Fatalf("trailing separator: %+v", d)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err == nil {
		if d := Resolve(s, filepath.Join(link, "sub")); d.Enabled || d.Source != "project" {
			t.Fatalf("through a symlink: %+v", d)
		}
	}
	if runtime.GOOS == "windows" {
		if d := Resolve(s, strings.ToUpper(work)); d.Enabled || d.Source != "project" {
			t.Fatalf("other case: %+v", d)
		}
	}
	rootedOnly := Settings{Auto: Auto{Enabled: true}, Projects: []Project{{Path: "/work/project", Enabled: false}}}
	if d := Resolve(rootedOnly, "/work/project"); d.Enabled {
		t.Fatalf("rooted path: %+v", d)
	}
}

// The user-wide off switch overrides every project; on or unset leaves project entries deciding.
func TestGlobalOffIsAKillSwitch(t *testing.T) {
	repo := t.TempDir()
	s := Settings{Projects: []Project{{Path: repo, Enabled: true, Visibility: "direct"}}}
	if d := Resolve(s, repo); !d.Enabled || d.Source != "project" {
		t.Fatalf("unset = %+v", d)
	}
	s.SetGlobal(GlobalOff)
	if d := Resolve(s, repo); d.Enabled || d.Source != "user" || d.Overridden != repo {
		t.Fatalf("off = %+v", d)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if d := ForHook(path, repo); d.Enabled {
		t.Fatalf("ForHook under the kill switch = %+v", d)
	}
	s.SetGlobal(GlobalOn)
	off := Settings{Auto: s.Auto, Projects: []Project{{Path: repo, Enabled: false}}}
	if d := Resolve(off, repo); d.Enabled || d.Source != "project" {
		t.Fatalf("project off under global on = %+v", d)
	}
}

// A file written before the switch had three states (enabled only) still reads.
func TestLoadReadsTheOldFormat(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"schema_version":"beacon.share_settings.v1","auto":{"enabled":true,"visibility":"private"}}`, GlobalOn},
		{`{"schema_version":"beacon.share_settings.v1","auto":{"enabled":false},"projects":[{"path":"` + filepath.ToSlash(repo) + `","enabled":true}]}`, GlobalUnset},
	} {
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(path)
		if err != nil || s.Global() != tc.want {
			t.Fatalf("%s: global = %q (err %v), want %q", tc.body, s.Global(), err, tc.want)
		}
		if tc.want == GlobalUnset {
			if d := Resolve(s, repo); !d.Enabled || d.Source != "project" {
				t.Fatalf("old project entry = %+v", d)
			}
		}
	}
}

// A repository's opt-out marker wins over the user's own entry for the same directory. A closer
// directory decides over a farther one: a user entry for a subdirectory enables it even when a
// parent's marker says off, and a subdirectory's off marker disables it under a parent's entry.
func TestMarkerOffWinsAtItsDirectoryAndTheClosestDecides(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	repo := t.TempDir()
	sub := filepath.Join(repo, "service")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Settings{Projects: []Project{{Path: repo, Enabled: true}}}
	if err := SaveMarker(repo, Marker{Auto: Auto{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if d := Resolve(s, repo); d.Enabled || d.Source != "marker" || d.Overridden != repo {
		t.Fatalf("user entry plus off marker = %+v", d)
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if d := ForHook(path, sub); d.Enabled {
		t.Fatalf("hook decision under the repository's opt-out = %+v", d)
	}
	// A user entry for the subdirectory is closer than the parent's marker.
	child := Settings{Projects: []Project{{Path: sub, Enabled: true}}}
	if d := Resolve(child, sub); !d.Enabled || d.Source != "project" || d.Project != sub {
		t.Fatalf("child entry under a parent's off marker = %+v", d)
	}
	// And a subdirectory's off marker is closer than the parent's entry.
	if err := os.Remove(MarkerPath(repo)); err != nil {
		t.Fatal(err)
	}
	if err := SaveMarker(sub, Marker{Auto: Auto{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if d := Resolve(s, sub); d.Enabled || d.Source != "marker" || d.Project != sub {
		t.Fatalf("child off marker under a parent's entry = %+v", d)
	}
}
