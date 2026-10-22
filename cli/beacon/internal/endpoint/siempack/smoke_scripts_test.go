package siempack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/falcon"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/gcs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/rapid7"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/s3"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sumo"
)

// The smoke-test script install-pack writes is the one an operator runs to prove the destination
// accepts events. Each pack's script content is tested through its render function (which
// print-config uses), so the installed file must be exactly that script for the configured log
// path -- not another asset installed under the script's name, which existence and mode checks
// would accept.
func TestInstalledSmokeScriptIsThePacksSmokeTest(t *testing.T) {
	packs := []struct {
		name    string
		script  string
		install func(outputDir, logPath string) error
		render  func(logPath string) (string, error)
	}{
		{"asymptote", "asymptote-ingest-smoke-test.sh", asymptote.InstallPack, asymptote.IngestSmokeTest},
		{"falcon", "falcon-hec-smoke-test.sh", falcon.InstallPack, falcon.SmokeTest},
		{"gcs", "gcs-upload-smoke-test.sh", gcs.InstallPack, gcs.UploadSmokeTest},
		{"rapid7", "rapid7-upload-smoke-test.sh", rapid7.InstallPack, rapid7.UploadSmokeTest},
		{"s3", "s3-upload-smoke-test.sh", s3.InstallPack, s3.UploadSmokeTest},
		{"sumo", "sumo-upload-smoke-test.sh", sumo.InstallPack, sumo.UploadSmokeTest},
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	for _, pack := range packs {
		t.Run(pack.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := pack.install(dir, logPath); err != nil {
				t.Fatalf("InstallPack: %v", err)
			}
			installed, err := os.ReadFile(filepath.Join(dir, pack.script))
			if err != nil {
				t.Fatal(err)
			}
			want, err := pack.render(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(installed) != want {
				t.Fatalf("installed %s is not the pack's smoke test for %q:\n%s", pack.script, logPath, installed)
			}
			if !strings.HasPrefix(want, "#!") || !strings.Contains(want, logPath) {
				t.Fatalf("%s smoke test is not a script tailing %q:\n%s", pack.name, logPath, want)
			}
		})
	}
}
