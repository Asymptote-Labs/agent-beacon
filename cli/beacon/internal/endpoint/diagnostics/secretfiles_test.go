package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func TestSecretFilesPrivateReportsAReadableToken(t *testing.T) {
	cfg := endpointconfig.Config{Collector: endpointconfig.Collector{ConfigPath: filepath.Join(t.TempDir(), "otelcol.yaml")}}
	readable := func(path string) ([]string, error) {
		if path == cfg.Collector.ConfigPath {
			return []string{"grants S-1-5-32-545 access (0x1200a9)"}, nil
		}
		return nil, nil
	}
	check := checkSecretFilesPrivate(cfg, readable)
	if check.Status != StatusFail || check.Evidence != "secret_readable_by_users" || check.Severity != SeverityHigh {
		t.Fatalf("a Users-readable token = %+v, want a high-severity failure", check)
	}
	if !strings.Contains(check.Message, cfg.Collector.ConfigPath) {
		t.Fatalf("the message does not name the readable file: %s", check.Message)
	}

	private := func(string) ([]string, error) { return nil, nil }
	if check := checkSecretFilesPrivate(cfg, private); check.Status != StatusOK || check.Evidence != "secret_private" {
		t.Fatalf("private files = %+v, want ok", check)
	}

	// A file that does not exist holds no token; one whose ACL cannot be read is unknown, not fine.
	missing := func(path string) ([]string, error) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	if check := checkSecretFilesPrivate(cfg, missing); check.Status != StatusOK {
		t.Fatalf("missing files = %+v, want ok", check)
	}
	unreadable := func(path string) ([]string, error) { return nil, errors.New("access is denied") }
	if check := checkSecretFilesPrivate(cfg, unreadable); check.Status != StatusWarn || check.Evidence != "acl_unreadable" {
		t.Fatalf("an unreadable ACL = %+v, want a warning", check)
	}
}
