package siempack

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func testPack() Pack {
	return Pack{
		Label:            "demo",
		DefaultLogPath:   "/var/log/beacon-agent/runtime.jsonl",
		DefaultOutputDir: "beacon-demo-pack",
		FS: fstest.MapFS{
			"pack/README.md":      {Data: []byte("readme")},
			"pack/run.sh.tmpl":    {Data: []byte("log={{LOG_PATH}}\n")},
			"pack/sample.jsonl":   {Data: []byte("{\"a\":1}\n")},
			"pack/conf.json.tmpl": {Data: []byte("{\"path\":\"{{LOG_PATH}}\"}")},
		},
		Assets: []Asset{
			{Source: "pack/README.md", Name: "README.md"},
			{Source: "pack/run.sh.tmpl", Name: "run.sh", TemplateLogPath: true},
			{Source: "pack/sample.jsonl", Name: "sample.jsonl"},
			{Source: "pack/conf.json.tmpl", Name: "conf.json", TemplateLogPath: true, JSONEscape: true},
		},
	}
}

func TestPackFilesReturnsUnrenderedContentWithFlags(t *testing.T) {
	files, err := testPack().Files()
	if err != nil {
		t.Fatalf("Files() error: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("Files() returned %d files, want 4", len(files))
	}
	// run.sh keeps the token unrendered; substitution is deferred to Install.
	if files[1].Name != "run.sh" || files[1].Content != "log={{LOG_PATH}}\n" || !files[1].TemplateLogPath {
		t.Fatalf("run.sh entry = %+v", files[1])
	}
	if !files[3].JSONEscape {
		t.Fatalf("conf.json should carry JSONEscape flag: %+v", files[3])
	}
}

func TestPackRenderSubstitutesLogPathAndDefaults(t *testing.T) {
	p := testPack()
	got, err := p.Render("pack/run.sh.tmpl", "/custom/path.jsonl")
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if got != "log=/custom/path.jsonl\n" {
		t.Fatalf("Render = %q", got)
	}
	def, err := p.Render("pack/run.sh.tmpl", "")
	if err != nil {
		t.Fatalf("Render default error: %v", err)
	}
	if def != "log="+p.DefaultLogPath+"\n" {
		t.Fatalf("Render with empty logPath = %q, want DefaultLogPath", def)
	}
}

func TestPackInstallWritesRenderedFilesWithModes(t *testing.T) {
	dir := t.TempDir()
	if err := testPack().Install(dir, "/custom/path.jsonl"); err != nil {
		t.Fatalf("Install error: %v", err)
	}

	run, err := os.ReadFile(filepath.Join(dir, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(run) != "log=/custom/path.jsonl\n" {
		t.Fatalf("installed run.sh = %q", run)
	}
	// .sh files are executable.
	info, err := os.Stat(filepath.Join(dir, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("run.sh mode = %v, want 0755", info.Mode().Perm())
	}

	// JSON-escaped substitution keeps the surrounding JSON valid.
	conf, err := os.ReadFile(filepath.Join(dir, "conf.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(conf) != "{\"path\":\"/custom/path.jsonl\"}" {
		t.Fatalf("installed conf.json = %q", conf)
	}
}

func TestPackReadAndMustReadWrapErrorsWithLabel(t *testing.T) {
	p := testPack().WithFS(fstest.MapFS{})
	_, err := p.Read("pack/README.md")
	if err == nil {
		t.Fatal("Read of missing asset should error")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustRead of missing asset should panic")
		}
	}()
	_ = p.MustRead("pack/README.md")
}
