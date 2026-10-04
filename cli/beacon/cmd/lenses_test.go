package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lensstore"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func writeTestLens(t *testing.T, dir, id string, version string) string {
	t.Helper()
	path := filepath.Join(dir, id+"-src.html")
	html := `<!doctype html><script type="application/beacon-lens+json">{"id":"` + id + `","title":"Test ` + id +
		`","version":` + version + `,"api":"beacon.lens.v1"}</script><script>window.beacon.getTrace()</script>`
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func useLensTestHome(t *testing.T) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	prev := lensesOpts
	lensesOpts.userMode, lensesOpts.systemMode, lensesOpts.force, lensesOpts.json = true, false, false, false
	t.Cleanup(func() { lensesOpts = prev })
}

func TestLensesAddListShowRemove(t *testing.T) {
	useLensTestHome(t)
	src := t.TempDir()

	cmd, buf := newCmd()
	if err := lensesAddCmd.RunE(cmd, []string{writeTestLens(t, src, "mine", "1")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "installed mine v1 (") {
		t.Fatalf("add output = %q", buf.String())
	}

	cmd, buf = newCmd()
	if err := lensesAddCmd.RunE(cmd, []string{writeTestLens(t, src, "mine", "2")}); err != nil || !strings.HasPrefix(buf.String(), "updated mine v1 -> v2") {
		t.Fatalf("update = %v, %q", err, buf.String())
	}
	cmd, _ = newCmd()
	if err := lensesAddCmd.RunE(cmd, []string{writeTestLens(t, src, "mine", "2")}); err == nil {
		t.Fatal("reinstalling the same version without --force succeeded")
	}

	lensesOpts.json = true
	cmd, buf = newCmd()
	if err := lensesListCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Lenses []lensListEntry `json:"lenses"`
		Store  string          `json:"store"`
	}
	if err := json.Unmarshal(buf.Bytes(), &listed); err != nil {
		t.Fatalf("list --json: %v\n%s", err, buf.String())
	}
	sources := map[string]string{}
	for _, l := range listed.Lenses {
		sources[l.ID] = l.Source
	}
	if sources["mine"] != "store" || sources["activity"] != "builtin" || listed.Store != lensstore.Dir(true) {
		t.Fatalf("list = %+v", listed)
	}
	lensesOpts.json = false

	cmd, buf = newCmd()
	if err := lensesShowCmd.RunE(cmd, []string{"mine"}); err != nil || !strings.Contains(buf.String(), `"version": 2`) || !strings.Contains(buf.String(), `"source": "store"`) {
		t.Fatalf("show = %v, %s", err, buf.String())
	}
	cmd, buf = newCmd()
	if err := lensesShowCmd.RunE(cmd, []string{"activity"}); err != nil || !strings.Contains(buf.String(), `"source": "builtin"`) {
		t.Fatalf("show builtin = %v, %s", err, buf.String())
	}
	// A built-in's manifest has the same shape as an installed lens's: the real manifest.
	var shown struct {
		Manifest map[string]interface{} `json:"manifest"`
	}
	if err := json.Unmarshal(buf.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Manifest["api"] != "beacon.lens.v1" || shown.Manifest["author"] != "Beacon" || shown.Manifest["source"] != nil {
		t.Fatalf("built-in manifest = %v", shown.Manifest)
	}

	cmd, buf = newCmd()
	if err := lensesRemoveCmd.RunE(cmd, []string{"mine"}); err != nil || !strings.HasPrefix(buf.String(), "removed mine") {
		t.Fatalf("remove = %v, %q", err, buf.String())
	}
	cmd, _ = newCmd()
	if err := lensesShowCmd.RunE(cmd, []string{"mine"}); err == nil {
		t.Fatal("show found a removed lens")
	}
}

func TestLensesAddRefusesBuiltinIDsAndInvalidFiles(t *testing.T) {
	useLensTestHome(t)
	src := t.TempDir()
	cmd, _ := newCmd()
	if err := lensesAddCmd.RunE(cmd, []string{writeTestLens(t, src, "files-changed", "1")}); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("built-in id = %v", err)
	}
	bad := filepath.Join(src, "bad.html")
	if err := os.WriteFile(bad, []byte("<p>no manifest</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lensesAddCmd.RunE(cmd, []string{bad}); err == nil {
		t.Fatal("an invalid lens installed")
	}
}

func TestLensesListWarnsAboutBrokenStoreFiles(t *testing.T) {
	useLensTestHome(t)
	store := lensstore.Dir(true)
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "junk.lens.html"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, buf := newCmd()
	if err := lensesListCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "junk.lens.html is not a valid lens") || !strings.Contains(buf.String(), "activity") {
		t.Fatalf("list output = %q", buf.String())
	}
}
