package lensstore

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func lensFile(t *testing.T, dir, name, id string, version int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	html := `<!doctype html><script type="application/beacon-lens+json">{"id":"` + id + `","title":"T","version":` +
		strconv.Itoa(version) + `,"api":"beacon.lens.v1"}</script><p>v` + strconv.Itoa(version) + `</p>`
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallListReadRemove(t *testing.T) {
	src := t.TempDir()
	store := filepath.Join(t.TempDir(), "lenses")

	// The stored name comes from the manifest id, not the source file name.
	lens, replaced, err := Install(store, lensFile(t, src, "whatever.html", "my-lens", 1), InstallOptions{})
	if err != nil || replaced != 0 {
		t.Fatalf("Install = %v, replaced %d", err, replaced)
	}
	if lens.Path != filepath.Join(store, "my-lens.lens.html") || lens.Manifest.ID != "my-lens" {
		t.Fatalf("installed %+v", lens)
	}
	if testenv.HasPOSIXFileModes() {
		if info, _ := os.Stat(store); info.Mode().Perm() != 0o700 {
			t.Errorf("store mode = %v, want 0700", info.Mode().Perm())
		}
		if info, _ := os.Stat(lens.Path); info.Mode().Perm() != 0o600 {
			t.Errorf("lens mode = %v, want 0600", info.Mode().Perm())
		}
	}

	lenses, problems, err := List(store)
	if err != nil || len(problems) != 0 || len(lenses) != 1 || lenses[0].Manifest.ID != "my-lens" {
		t.Fatalf("List = %+v, %+v, %v", lenses, problems, err)
	}
	html, manifest, err := Read(store, "my-lens")
	if err != nil || manifest.Version != 1 || !strings.Contains(string(html), "<p>v1</p>") {
		t.Fatalf("Read = %v, %+v", err, manifest)
	}

	path, err := Remove(store, "my-lens")
	if err != nil || path != lens.Path {
		t.Fatalf("Remove = %q, %v", path, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("removed lens still exists")
	}
	if _, err := Remove(store, "my-lens"); err == nil || !strings.Contains(err.Error(), "no installed lens") {
		t.Fatalf("second Remove = %v", err)
	}
	// No temp files are left behind.
	entries, _ := os.ReadDir(store)
	if len(entries) != 0 {
		t.Fatalf("store holds %d leftover entries", len(entries))
	}
}

func TestInstallReplacesOnlyNewerVersionsUnlessForced(t *testing.T) {
	src := t.TempDir()
	store := t.TempDir()
	if _, _, err := Install(store, lensFile(t, src, "a.html", "my-lens", 2), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(store, lensFile(t, src, "b.html", "my-lens", 2), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "bump the version") {
		t.Fatalf("same version = %v, want refusal", err)
	}
	if _, _, err := Install(store, lensFile(t, src, "c.html", "my-lens", 1), InstallOptions{}); err == nil {
		t.Fatal("an older version replaced a newer one")
	}
	if _, replaced, err := Install(store, lensFile(t, src, "d.html", "my-lens", 3), InstallOptions{}); err != nil || replaced != 2 {
		t.Fatalf("newer version = %v, replaced %d", err, replaced)
	}
	if _, replaced, err := Install(store, lensFile(t, src, "e.html", "my-lens", 1), InstallOptions{Force: true}); err != nil || replaced != 3 {
		t.Fatalf("forced downgrade = %v, replaced %d", err, replaced)
	}
	if _, manifest, _ := Read(store, "my-lens"); manifest.Version != 1 {
		t.Fatalf("stored version = %d, want the forced 1", manifest.Version)
	}
}

func TestInstallRefusals(t *testing.T) {
	src := t.TempDir()
	store := t.TempDir()
	if _, _, err := Install(store, lensFile(t, src, "a.html", "activity", 1), InstallOptions{Reserved: map[string]bool{"activity": true}}); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("reserved id = %v", err)
	}
	bad := filepath.Join(src, "bad.html")
	if err := os.WriteFile(bad, []byte("<p>no manifest</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(store, bad, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "not a valid lens") {
		t.Fatalf("invalid lens = %v", err)
	}
	if _, _, err := Install(store, filepath.Join(src, "absent.html"), InstallOptions{}); err == nil {
		t.Fatal("a missing file installed")
	}
}

func TestListReportsBrokenFilesWithoutHidingTheRest(t *testing.T) {
	store := t.TempDir()
	lensFile(t, store, "good.lens.html", "good", 1)
	lensFile(t, store, "renamed.lens.html", "other-id", 1) // name and id disagree
	if err := os.WriteFile(filepath.Join(store, "junk.lens.html"), []byte("not a lens"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	lenses, problems, err := List(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(lenses) != 1 || lenses[0].Manifest.ID != "good" {
		t.Fatalf("lenses = %+v", lenses)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %+v, want the renamed and junk files", problems)
	}
	if _, _, err := Read(store, "renamed"); err == nil || !strings.Contains(err.Error(), "declares id") {
		t.Fatalf("Read of a misnamed file = %v", err)
	}

	missing, problems, err := List(filepath.Join(store, "absent"))
	if err != nil || missing != nil || problems != nil {
		t.Fatalf("missing store = %v, %v, %v", missing, problems, err)
	}
}

func TestIDsCannotLeaveTheStore(t *testing.T) {
	store := t.TempDir()
	for _, id := range []string{"../escape", "a/b", `a\b`, "", ".hidden", "UPPER"} {
		if _, err := PathFor(store, id); err == nil {
			t.Errorf("PathFor(%q) accepted", id)
		}
		if _, _, err := Read(store, id); err == nil {
			t.Errorf("Read(%q) accepted", id)
		}
		if _, err := Remove(store, id); err == nil {
			t.Errorf("Remove(%q) accepted", id)
		}
	}
}
