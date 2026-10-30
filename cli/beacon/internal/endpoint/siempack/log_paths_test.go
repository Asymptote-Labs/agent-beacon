package siempack_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/syntax"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/cloudwatch"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/datadog"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/elastic"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/falcon"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/gcs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/rapid7"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/s3"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sumo"
)

// logPaths are the path shapes every pack must carry through unchanged: the system default on each
// platform, and paths full of the syntax each format quotes.
var logPaths = []struct{ name, path string }{
	{"posix", "/var/log/beacon-agent/runtime.jsonl"},
	{"windows", `C:\Users\x\runtime.jsonl`},
	{"escape_sequences", `C:\new\temp\runtime.jsonl`},
	{"syntax", "/tmp/a: b/\"q\"/$(touch pwned)/`x`/}{/runtime.jsonl"},
	{"spaces_unicode", "/tmp/telemetry logs/日本語/runtime.jsonl"},
	{"controls", "/tmp/beacon\n\r\t\b\f\x01\x1f\x7f\u0085\u009f\ufeff\uffff/runtime.jsonl"},
}

func TestTOMLLogPathsRoundTrip(t *testing.T) {
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
	for _, pack := range packs {
		for _, path := range logPaths {
			t.Run(pack.name+"/"+path.name, func(t *testing.T) {
				want := map[string][]string{"beacon_runtime": {path.path}}
				if pack.inventory {
					want["beacon_inventory"] = []string{siempack.InventoryLogPath(path.path)}
				}
				if pack.backfill {
					want["beacon_backfill"] = []string{siempack.BackfillLogPath(path.path)}
				}
				installed := installedFile(t, pack.install, path.path, "vector.toml")
				assertSourcePaths(t, installed, want)
				if pack.render != nil {
					assertRendered(t, pack.render, path.path, installed)
				}
			})
		}
	}
}

func TestYAMLLogPathsRoundTrip(t *testing.T) {
	files := []struct {
		pack    string
		install func(string, string) error
		name    string
		render  func(string) (string, error)
	}{
		{"datadog", datadog.InstallPack, "conf.yaml", datadog.ConfigSnippet},
		{"elastic", elastic.InstallPack, "filebeat.yml", elastic.InputSnippet},
		{"elastic", elastic.InstallPack, "elastic-agent-standalone.yml", nil},
	}
	for _, file := range files {
		for _, path := range logPaths {
			t.Run(file.name+"/"+path.name, func(t *testing.T) {
				installed := installedFile(t, file.install, path.path, file.name)
				var doc any
				if err := yaml.Unmarshal([]byte(installed), &doc); err != nil {
					t.Fatalf("invalid rendered YAML: %v\n%s", err, installed)
				}
				if got := stringsUnder(doc, "path", "paths"); !reflect.DeepEqual(got, []string{path.path}) {
					t.Fatalf("log paths = %q, want %q", got, path.path)
				}
				if file.render != nil {
					assertRendered(t, file.render, path.path, installed)
				}
			})
		}
	}
}

func TestJSONLogPathsRoundTrip(t *testing.T) {
	for _, path := range logPaths {
		t.Run(path.name, func(t *testing.T) {
			installed := installedFile(t, sentinel.InstallPack, path.path, "dcr-template.json")
			var doc any
			if err := json.Unmarshal([]byte(installed), &doc); err != nil {
				t.Fatalf("invalid rendered JSON: %v", err)
			}
			if got := stringsUnder(doc, "filePatterns"); !reflect.DeepEqual(got, []string{path.path}) {
				t.Fatalf("filePatterns = %q, want %q", got, path.path)
			}
		})
	}
}

// The smoke-test scripts take the log path as the default of "${BEACON_LOG:-...}". Each script must
// parse, and its defaults must evaluate to the paths without running anything inside them.
func TestShellLogPathsRoundTrip(t *testing.T) {
	// The smoke-test scripts run on macOS and Linux hosts. Git for Windows' sh drops carriage
	// returns from its input, so it cannot read these values back exactly.
	if runtime.GOOS == "windows" {
		t.Skip("the smoke-test scripts are POSIX shell scripts for macOS and Linux")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell to run the smoke-test scripts")
	}
	scripts := []struct {
		pack      string
		install   func(string, string) error
		name      string
		render    func(string) (string, error)
		inventory bool
	}{
		{"asymptote", asymptote.InstallPack, "asymptote-ingest-smoke-test.sh", asymptote.IngestSmokeTest, true},
		{"falcon", falcon.InstallPack, "falcon-hec-smoke-test.sh", falcon.SmokeTest, false},
		{"gcs", gcs.InstallPack, "gcs-upload-smoke-test.sh", gcs.UploadSmokeTest, true},
		{"rapid7", rapid7.InstallPack, "rapid7-upload-smoke-test.sh", rapid7.UploadSmokeTest, false},
		{"s3", s3.InstallPack, "s3-upload-smoke-test.sh", s3.UploadSmokeTest, true},
		{"sumo", sumo.InstallPack, "sumo-upload-smoke-test.sh", sumo.UploadSmokeTest, false},
	}
	for _, script := range scripts {
		for _, path := range logPaths {
			t.Run(script.pack+"/"+path.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := script.install(dir, path.path); err != nil {
					t.Fatal(err)
				}
				scriptPath := filepath.Join(dir, script.name)
				if out, err := exec.Command(sh, "-n", scriptPath).CombinedOutput(); err != nil {
					t.Fatalf("sh -n: %v\n%s", err, out)
				}
				content, err := os.ReadFile(scriptPath)
				if err != nil {
					t.Fatal(err)
				}
				assertRendered(t, script.render, path.path, string(content))

				want := map[string]string{"BEACON_LOG": path.path}
				if script.inventory {
					want["BEACON_INVENTORY_LOG"] = siempack.InventoryLogPath(path.path)
				}
				// Run only the default assignments: the rest of the script needs credentials.
				defaults, names := defaultAssignments(t, string(content), want)
				for _, name := range names {
					defaults += "\nprintf '%s\\0' \"$" + name + "\""
				}
				cwd := t.TempDir()
				cmd := exec.Command(sh, "-c", defaults)
				cmd.Dir = cwd
				cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil || stderr.Len() > 0 {
					t.Fatalf("defaults failed: %v\n%s\n%s", err, stderr.String(), defaults)
				}
				got := strings.Split(strings.TrimSuffix(stdout.String(), "\x00"), "\x00")
				for i, name := range names {
					if got[i] != want[name] {
						t.Errorf("%s = %q, want %q", name, got[i], want[name])
					}
				}
				if entries, _ := os.ReadDir(cwd); len(entries) > 0 {
					t.Fatalf("evaluating the defaults ran a command that created %s", entries[0].Name())
				}
			})
		}
	}
}

// defaultAssignments returns the script's top-level assignments to the variables in want, in order.
func defaultAssignments(t *testing.T, content string, want map[string]string) (string, []string) {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(content), "")
	if err != nil {
		t.Fatal(err)
	}
	var lines, names []string
	for _, stmt := range file.Stmts {
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 0 || len(call.Assigns) != 1 {
			continue
		}
		if name := call.Assigns[0].Name.Value; want[name] != "" {
			lines = append(lines, content[stmt.Pos().Offset():stmt.End().Offset()])
			names = append(names, name)
		}
	}
	if len(names) != len(want) {
		t.Fatalf("found assignments to %q, want %d", names, len(want))
	}
	return strings.Join(lines, "\n"), names
}

func installedFile(t *testing.T, install func(string, string) error, logPath, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := install(dir, logPath); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// assertRendered checks that the printed config is the installed file.
func assertRendered(t *testing.T, render func(string) (string, error), logPath, installed string) {
	t.Helper()
	rendered, err := render(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != installed {
		t.Fatalf("rendered config and installed file differ:\n%s\n---\n%s", rendered, installed)
	}
}

// stringsUnder collects the strings stored under any of keys in a decoded YAML or JSON document.
func stringsUnder(node any, keys ...string) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		for key, value := range n {
			if !slices.Contains(keys, key) {
				out = append(out, stringsUnder(value, keys...)...)
				continue
			}
			switch v := value.(type) {
			case string:
				out = append(out, v)
			case []any:
				for _, item := range v {
					if s, ok := item.(string); ok {
						out = append(out, s)
					}
				}
			}
		}
	case []any:
		for _, item := range n {
			out = append(out, stringsUnder(item, keys...)...)
		}
	}
	return out
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

func TestOtherFormatsGetTheLogPathVerbatim(t *testing.T) {
	path := `/tmp/beacon"quoted\temp/runtime.jsonl`
	const content = "path={{LOG_PATH}}; inventory={{INVENTORY_LOG_PATH}}; backfill={{BACKFILL_LOG_PATH}}"
	want := "path=" + path + "; inventory=" + siempack.InventoryLogPath(path) + "; backfill=" + siempack.BackfillLogPath(path)
	for _, name := range []string{"README.md", "log.xml"} {
		t.Run(name, func(t *testing.T) {
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
	t.Run("ordinary_posix_toml", func(t *testing.T) {
		const content = "include = [\"{{LOG_PATH}}\"]\n"
		pack := siempack.Pack{FS: fstest.MapFS{"pack/vector.toml.tmpl": {Data: []byte(content)}}}
		rendered, err := pack.Render("pack/vector.toml.tmpl", "/var/log/beacon-agent/runtime.jsonl")
		if err != nil || rendered != strings.ReplaceAll(content, "{{LOG_PATH}}", "/var/log/beacon-agent/runtime.jsonl") {
			t.Fatalf("ordinary POSIX render changed: %q, %v", rendered, err)
		}
	})
}
