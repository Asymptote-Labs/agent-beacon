package siempack_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/cloudwatch"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/datadog"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/elastic"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/falcon"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/gcs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/rapid7"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/s3"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sumo"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/wazuh"
)

// `beacon endpoint <destination> install-pack` prints "content pack written to <dir>" whenever
// InstallPack returns nil, so a pack that swallows a failed write tells the operator a forwarder
// config exists when it does not. Both failures are provoked with file-vs-directory conflicts rather
// than permission bits, so they fail the same way as root and on Windows.
func TestInstallPackReportsAnOutputItCouldNotWrite(t *testing.T) {
	packs := []struct {
		name    string
		install func(outputDir, logPath string) error
	}{
		{"asymptote", asymptote.InstallPack},
		{"cloudwatch", cloudwatch.InstallPack},
		{"datadog", datadog.InstallPack},
		{"elastic", elastic.InstallPack},
		{"falcon", falcon.InstallPack},
		{"gcs", gcs.InstallPack},
		{"rapid7", rapid7.InstallPack},
		{"s3", s3.InstallPack},
		{"sentinel", sentinel.InstallPack},
		{"sumo", sumo.InstallPack},
		{"wazuh", wazuh.InstallPack},
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	for _, pack := range packs {
		t.Run(pack.name, func(t *testing.T) {
			t.Run("output directory cannot be created", func(t *testing.T) {
				blocker := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := pack.install(filepath.Join(blocker, "pack"), logPath); err == nil {
					t.Fatal("InstallPack under a regular file returned nil; install-pack would report success")
				}
			})
			t.Run("pack file cannot be written", func(t *testing.T) {
				outputDir := t.TempDir()
				// Every pack writes README.md; a directory in its place makes that one write fail.
				if err := os.Mkdir(filepath.Join(outputDir, "README.md"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := pack.install(outputDir, logPath); err == nil {
					t.Fatal("InstallPack returned nil although README.md could not be written; install-pack would report success")
				}
			})
		})
	}
}
