package siempack_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pelletier/go-toml/v2"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/cloudwatch"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/falcon"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/gcs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/rapid7"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/s3"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sumo"
)

func TestVectorPackLogPathsRoundTrip(t *testing.T) {
	paths := []struct{ name, path string }{
		{"posix", "/var/log/beacon-agent/runtime.jsonl"},
		{"windows", `C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl`},
		{"escape_sequences", `C:\new\temp\runtime.jsonl`},
		{"quotes", `/tmp/beacon"quoted/runtime.jsonl`},
		{"spaces_unicode", "/tmp/telemetry logs/日本語/runtime.jsonl"},
		{"backslash", `/tmp/beacon\temp/runtime.jsonl`},
		{"controls", "/tmp/beacon\n\r\t\b\f\x01\x1f\x7f/runtime.jsonl"},
	}
	packs := []struct {
		name      string
		install   func(string, string) error
		render    func(string) (string, error)
		inventory bool
		backfill  bool
	}{
		{name: "asymptote", install: asymptote.InstallPack, render: asymptote.VectorConfig, inventory: true, backfill: true},
		{name: "cloudwatch", install: cloudwatch.InstallPack, render: cloudwatch.ConfigSnippet},
		{name: "falcon", install: falcon.InstallPack, render: falcon.ConfigSnippet},
		{name: "gcs", install: gcs.InstallPack, inventory: true},
		{name: "rapid7", install: rapid7.InstallPack},
		{name: "s3", install: s3.InstallPack, inventory: true},
		{name: "sentinel", install: sentinel.InstallPack},
		{name: "sumo", install: sumo.InstallPack},
	}
	// Escaping is shared siempack code, so every path shape runs through the asymptote pack (which
	// renders all three sources). The other packs only need the hardest shapes to prove their
	// templates place paths in TOML basic strings.
	hardest := []struct{ name, path string }{paths[1], paths[6]}
	for _, pack := range packs {
		t.Run(pack.name, func(t *testing.T) {
			cases := hardest
			if pack.name == "asymptote" {
				cases = paths
			}
			for _, path := range cases {
				t.Run(path.name, func(t *testing.T) {
					want := map[string][]string{"beacon_runtime": {path.path}}
					if pack.inventory {
						want["beacon_inventory"] = []string{filepath.Join(filepath.Dir(path.path), "inventory_state.jsonl")}
					}
					if pack.backfill {
						want["beacon_backfill"] = []string{filepath.Join(filepath.Dir(path.path), "cloud-backfill.jsonl")}
					}
					dir := t.TempDir()
					if err := pack.install(dir, path.path); err != nil {
						t.Fatal(err)
					}
					content, err := os.ReadFile(filepath.Join(dir, "vector.toml"))
					if err != nil {
						t.Fatal(err)
					}
					assertSourcePaths(t, string(content), want)
					if pack.render != nil {
						rendered, err := pack.render(path.path)
						if err != nil {
							t.Fatal(err)
						}
						assertSourcePaths(t, rendered, want)
						if rendered != string(content) {
							t.Fatal("rendered snippet and installed config differ")
						}
					}
				})
			}
		})
	}
}

func assertSourcePaths(t *testing.T, content string, want map[string][]string) {
	t.Helper()
	var config struct {
		Sources map[string]struct {
			Include []string `toml:"include"`
		} `toml:"sources"`
	}
	if err := toml.Unmarshal([]byte(content), &config); err != nil {
		t.Fatalf("invalid rendered TOML: %v", err)
	}
	for source, paths := range want {
		if got := config.Sources[source].Include; !reflect.DeepEqual(got, paths) {
			t.Errorf("%s.include = %q, want %q", source, got, paths)
		}
	}
}

func TestPackTOMLRenderingPreservesSiblingPathsAndArchiveGlob(t *testing.T) {
	const content = "[sources.beacon_runtime]\ninclude = [\"{{LOG_PATH}}\", \"{{LOG_PATH}}.*\"]\n" +
		"[sources.beacon_inventory]\ninclude = [\"{{INVENTORY_LOG_PATH}}\"]\n" +
		"[sources.beacon_backfill]\ninclude = [\"{{BACKFILL_LOG_PATH}}\"]\n"
	path := `/tmp/beacon"quoted\temp/runtime.jsonl`
	pack := siempack.Pack{
		DefaultLogPath: path,
		FS:             fstest.MapFS{"pack/vector.toml.tmpl": {Data: []byte(content)}},
		Assets:         []siempack.Asset{{Source: "pack/vector.toml.tmpl", Name: "vector.toml", TemplateLogPath: true}},
	}
	want := map[string][]string{
		"beacon_runtime":   {path, path + ".*"},
		"beacon_inventory": {filepath.Join(filepath.Dir(path), "inventory_state.jsonl")},
		"beacon_backfill":  {filepath.Join(filepath.Dir(path), "cloud-backfill.jsonl")},
	}
	rendered, err := pack.Render("pack/vector.toml.tmpl", "")
	if err != nil {
		t.Fatal(err)
	}
	assertSourcePaths(t, rendered, want)
	dir := t.TempDir()
	if err := pack.Install(dir, ""); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(filepath.Join(dir, "vector.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != rendered {
		t.Fatal("default path differs between render and install")
	}
}

func TestPathRenderingPreservesOtherFormats(t *testing.T) {
	path := `/tmp/beacon"quoted\temp/runtime.jsonl`
	for _, name := range []string{"run.sh", "conf.yaml", "README.md", "log.xml"} {
		t.Run(name, func(t *testing.T) {
			const content = "path={{LOG_PATH}}; inventory={{INVENTORY_LOG_PATH}}; backfill={{BACKFILL_LOG_PATH}}"
			want := siempack.RenderLogPath(content, path)
			pack := siempack.Pack{FS: fstest.MapFS{"pack/" + name + ".tmpl": {Data: []byte(content)}}}
			rendered, err := pack.Render("pack/"+name+".tmpl", path)
			if err != nil || rendered != want {
				t.Fatalf("Render = %q, %v; want %q", rendered, err, want)
			}
			dir := t.TempDir()
			if err := siempack.Install(dir, []siempack.File{{Name: name, Content: content, TemplateLogPath: true}}, path); err != nil {
				t.Fatal(err)
			}
			installed, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || string(installed) != want {
				t.Fatalf("Install = %q, %v; want %q", installed, err, want)
			}
		})
	}
	t.Run("json", func(t *testing.T) {
		dir := t.TempDir()
		if err := siempack.Install(dir, []siempack.File{{Name: "conf.json", Content: `{"path":"{{LOG_PATH}}"}`, TemplateLogPath: true, JSONEscape: true}}, path); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(dir, "conf.json"))
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Path string }
		if err := json.Unmarshal(content, &got); err != nil || got.Path != path {
			t.Fatalf("JSON path = %q, %v; want %q", got.Path, err, path)
		}
	})
	t.Run("ordinary_posix", func(t *testing.T) {
		const content = "include = [\"{{LOG_PATH}}\"]\n"
		pack := siempack.Pack{FS: fstest.MapFS{"pack/vector.toml.tmpl": {Data: []byte(content)}}}
		rendered, err := pack.Render("pack/vector.toml.tmpl", "/var/log/beacon-agent/runtime.jsonl")
		if err != nil || rendered != strings.ReplaceAll(content, "{{LOG_PATH}}", "/var/log/beacon-agent/runtime.jsonl") {
			t.Fatalf("ordinary POSIX render changed: %q, %v", rendered, err)
		}
	})
}
