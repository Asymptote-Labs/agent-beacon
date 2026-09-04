package asymptote

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestVectorConfigForwardsBothStreamsOverHTTPWithABearerSecret(t *testing.T) {
	got, err := VectorConfig("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatalf("VectorConfig returned error: %v", err)
	}
	for _, want := range []string{
		`include = ["/tmp/beacon/runtime.jsonl"]`,
		`include = ["/tmp/beacon/inventory_state.jsonl"]`,
		`read_from = "end"`,
		`read_from = "beginning"`,
		`[secret.beacon]`,
		`type = "file"`,
		`path = "${BEACON_ASYMPTOTE_SECRETS_FILE}"`,
		`data_dir = "${BEACON_ASYMPTOTE_DATA_DIR:-/var/lib/vector/beacon-asymptote}"`,
		`type = "http"`,
		`uri = "${BEACON_ASYMPTOTE_INGEST_URL}/v1/ingest/runtime"`,
		`uri = "${BEACON_ASYMPTOTE_INGEST_URL}/v1/ingest/inventory"`,
		`uri = "${BEACON_ASYMPTOTE_INGEST_URL}/v1/ingest/health"`,
		`strategy = "bearer"`,
		`token = "SECRET[beacon.device_key]"`,
		`compression = "gzip"`,
		`codec = "text"`,
		`method = "newline_delimited"`,
		`Content-Type = "application/x-ndjson"`,
		`max_bytes = 5000000`,
		`max_events = 5000`,
		`type = "disk"`,
		`when_full = "block"`,
		`retry_max_duration_secs = 300`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("vector config missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"{{LOG_PATH}}", "{{INVENTORY_LOG_PATH}}", "bcn_device", "parse_json"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("vector config must not contain %q:\n%s", forbidden, got)
		}
	}
	// Only the secret reference may carry the key; a literal token would defeat the
	// secrets file.
	if strings.Count(got, "SECRET[beacon.device_key]") != 2 {
		t.Fatalf("expected the two sinks to reference the secret exactly once each:\n%s", got)
	}
}

func TestIngestSmokeTestChecksCredentialBeforePosting(t *testing.T) {
	got, err := IngestSmokeTest("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatalf("IngestSmokeTest returned error: %v", err)
	}
	for _, want := range []string{
		`BEACON_LOG="${BEACON_LOG:-/tmp/beacon/runtime.jsonl}"`,
		`BEACON_INVENTORY_LOG="${BEACON_INVENTORY_LOG:-/tmp/beacon/inventory_state.jsonl}"`,
		"BEACON_ASYMPTOTE_INGEST_URL",
		"BEACON_ASYMPTOTE_SECRETS_FILE",
		"https://*)",
		"/v1/ingest/health",
		"/v1/ingest/runtime",
		"/v1/ingest/inventory",
		"Content-Encoding: gzip",
		"Content-Type: application/x-ndjson",
		"-H @",
		"validation only",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("smoke test missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("smoke test still contains template token:\n%s", got)
	}
	if strings.Contains(got, `-H "Authorization: Bearer $`) {
		t.Fatalf("smoke test must not pass the device key on the curl command line:\n%s", got)
	}
}

func TestInstallPackWritesExpectedFilesWithSafeModes(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"README.md", "asymptote-ingest-smoke-test.sh", "sample-event.jsonl", "vector.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	script, err := os.Stat(filepath.Join(dir, "asymptote-ingest-smoke-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if script.Mode().Perm()&0111 == 0 {
		t.Fatalf("smoke test should be executable, mode=%s", script.Mode())
	}
	vectorPath := filepath.Join(dir, "vector.toml")
	vectorConfig, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vectorConfig), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated vector config missing configured log path: %s", vectorConfig)
	}
	if strings.Contains(string(vectorConfig), "bcn_device") {
		t.Fatalf("generated vector config must never contain a device key: %s", vectorConfig)
	}
	info, err := os.Stat(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("generated vector config should be 0644, mode=%s", info.Mode().Perm())
	}
}

func TestSampleEventsAreValidBeaconEventsForThisDestination(t *testing.T) {
	sample := mustRead("pack/sample-event.jsonl")
	scanner := bufio.NewScanner(strings.NewReader(sample))
	lines := 0
	sawDestination := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines++
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("sample line %d is not JSON: %v", lines, err)
		}
		for _, key := range []string{"timestamp", "vendor", "product", "schema_version", "event"} {
			if _, ok := event[key]; !ok {
				t.Fatalf("sample line %d missing %q", lines, key)
			}
		}
		if event["vendor"] != "beacon" || event["product"] != "endpoint-agent" {
			t.Fatalf("sample line %d has wrong vendor/product: %v", lines, event)
		}
		if dest, ok := event["destination"].(map[string]any); ok {
			sawDestination = true
			if dest["type"] != "asymptote" || dest["mode"] != "asymptote_managed_http" {
				t.Fatalf("sample destination = %v", dest)
			}
		}
	}
	if lines < 2 || !sawDestination {
		t.Fatalf("expected several sample events including a validation event, got %d lines (destination seen: %t)", lines, sawDestination)
	}
}

func TestRenderVectorConfigReplacesEveryEnvironmentReference(t *testing.T) {
	got, err := RenderVectorConfig(RenderOptions{
		LogPath:     "/Users/me/.beacon/endpoint/logs/runtime.jsonl",
		IngestURL:   "https://ingest.example.test/",
		SecretsFile: "/Users/me/.beacon/endpoint/asymptote/vector-secrets.json",
		DataDir:     "/Users/me/.beacon/endpoint/asymptote/vector-data",
	})
	if err != nil {
		t.Fatalf("RenderVectorConfig returned error: %v", err)
	}
	for _, want := range []string{
		`data_dir = "/Users/me/.beacon/endpoint/asymptote/vector-data"`,
		`path = "/Users/me/.beacon/endpoint/asymptote/vector-secrets.json"`,
		`uri = "https://ingest.example.test/v1/ingest/runtime"`,
		`uri = "https://ingest.example.test/v1/ingest/inventory"`,
		`uri = "https://ingest.example.test/v1/ingest/health"`,
		`include = ["/Users/me/.beacon/endpoint/logs/runtime.jsonl"]`,
		`token = "SECRET[beacon.device_key]"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "${") {
		t.Fatalf("rendered config still has environment references:\n%s", got)
	}
}

func TestRenderVectorConfigRefusesInsecureOrIncompleteInput(t *testing.T) {
	if _, err := RenderVectorConfig(RenderOptions{LogPath: "/tmp/r.jsonl", IngestURL: "http://ingest.example.test", SecretsFile: "/s", DataDir: "/d"}); !errors.Is(err, ErrInsecureIngestURL) {
		t.Fatalf("expected ErrInsecureIngestURL, got %v", err)
	}
	if _, err := RenderVectorConfig(RenderOptions{LogPath: "/tmp/r.jsonl", IngestURL: "https://ingest.example.test"}); !errors.Is(err, ErrIncompleteRender) {
		t.Fatalf("expected ErrIncompleteRender, got %v", err)
	}
}

func TestSecretsFileContentIsTheShapeTheTemplateReads(t *testing.T) {
	content := SecretsFileContent(`bcn_device_abcdefgh_` + strings.Repeat("x", 43))
	var parsed map[string]string
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("secrets content is not JSON: %v", err)
	}
	if !strings.HasPrefix(parsed[SecretsKey], "bcn_device_abcdefgh_") {
		t.Fatalf("secrets content = %q", content)
	}
	quoted := SecretsFileContent(`a"b\c`)
	if err := json.Unmarshal([]byte(quoted), &parsed); err != nil || parsed[SecretsKey] != `a"b\c` {
		t.Fatalf("secrets content must escape the key: %q (%v)", quoted, err)
	}
}

func TestFilesFromFSPropagatesReadErrors(t *testing.T) {
	fsys := fstest.MapFS{"pack/README.md": &fstest.MapFile{Data: []byte("readme")}}
	if _, err := filesFromFS(fsys); err == nil || !strings.Contains(err.Error(), "asymptote pack asset") {
		t.Fatalf("expected a labelled asset read error, got %v", err)
	}
}
