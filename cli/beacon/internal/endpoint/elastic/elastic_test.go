package elastic

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestInputSnippetUsesConfiguredPath(t *testing.T) {
	got, err := InputSnippet("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatalf("InputSnippet returned unexpected error: %v", err)
	}
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("snippet did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("snippet still contains template token: %s", got)
	}
	if !strings.Contains(got, "filestream") {
		t.Fatalf("snippet should use Filebeat filestream input: %s", got)
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{
		"README.md",
		"filebeat.yml",
		"elastic-agent-standalone.yml",
		"ilm-policy.json",
		"component-template-mappings.json",
		"component-template-settings.json",
		"index-template.json",
		"ingest-pipeline.json",
		"kibana-assets.ndjson",
		"docker-compose.yml",
		"sample-event.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	filebeat, err := os.ReadFile(filepath.Join(dir, "filebeat.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(filebeat), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated filebeat.yml missing configured log path: %s", filebeat)
	}
	for _, emptyCredential := range []string{`api_key: "${ES_API_KEY:}"`, `username: "${ES_USERNAME:}"`, `password: "${ES_PASSWORD:}"`} {
		if strings.Contains(string(filebeat), emptyCredential) {
			t.Fatalf("generated filebeat.yml should not emit empty credential defaults: %s", emptyCredential)
		}
	}
	compose, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "${BEACON_LOG_DIR}:${BEACON_LOG_DIR}:ro") {
		t.Fatalf("docker-compose.yml should mount the log directory at the same path: %s", compose)
	}
}

func TestPackJSONFilesAreValid(t *testing.T) {
	for _, path := range []string{
		"pack/ilm-policy.json",
		"pack/component-template-mappings.json",
		"pack/component-template-settings.json",
		"pack/index-template.json",
		"pack/ingest-pipeline.json",
	} {
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(mustRead(path)), &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
	}
}

func TestKibanaAssetsAreParseableNDJSON(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/kibana-assets.ndjson")))
	line := 0
	var foundDataView, foundPromptColumn bool
	for scanner.Scan() {
		line++
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("kibana-assets.ndjson line %d is not valid JSON: %v", line, err)
		}
		if doc["type"] == "" || doc["id"] == "" {
			t.Fatalf("kibana-assets.ndjson line %d missing type/id: %#v", line, doc)
		}
		if doc["type"] == "index-pattern" {
			attrs, ok := doc["attributes"].(map[string]interface{})
			if !ok {
				t.Fatalf("kibana data view missing attributes: %#v", doc)
			}
			if attrs["name"] == "Beacon Endpoint Events" {
				foundDataView = true
			}
		}
		if doc["type"] == "search" {
			attrs, ok := doc["attributes"].(map[string]interface{})
			if !ok {
				t.Fatalf("kibana search missing attributes: %#v", doc)
			}
			columns, ok := attrs["columns"].([]interface{})
			if !ok {
				t.Fatalf("kibana search missing columns: %#v", attrs)
			}
			for _, column := range columns {
				if column == "beacon.prompt.text" {
					foundPromptColumn = true
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if line == 0 {
		t.Fatal("kibana-assets.ndjson was empty")
	}
	if !foundDataView {
		t.Fatal("kibana-assets.ndjson should include the Beacon Endpoint Events data view")
	}
	if !foundPromptColumn {
		t.Fatal("kibana saved search should expose beacon.prompt.text so prompts are visible in Elastic")
	}
}

func TestPackREADMEUsesIngestedFieldNames(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon.product:endpoint-agent",
		"beacon.prompt.text",
		"beacon.harness.name",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing ingested field query %q", want)
		}
	}
	if strings.Contains(readme, "product:endpoint-agent") && !strings.Contains(readme, "beacon.product:endpoint-agent") {
		t.Fatalf("pack README should not use raw pre-ingest product field query")
	}
}

func TestSampleEventsCoverHookAndOTelShapes(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/sample-event.jsonl")))
	var sawHook, sawOTel bool
	for scanner.Scan() {
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("sample-event.jsonl is not valid JSONL: %v", err)
		}
		if raw, ok := doc["raw"].(map[string]interface{}); ok && raw["otel_signal"] != nil {
			sawOTel = true
		}
		if _, ok := doc["tool"].(map[string]interface{}); ok {
			sawHook = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawHook || !sawOTel {
		t.Fatalf("sample events should include hook-rich and OTel shapes, sawHook=%t sawOTel=%t", sawHook, sawOTel)
	}
}

func TestIngestPipelineMentionsKeyMappings(t *testing.T) {
	pipeline := mustRead("pack/ingest-pipeline.json")
	for _, want := range []string{"event.duration", "'otel'", "host.hostname", "process.command_line", "gen_ai"} {
		if !strings.Contains(pipeline, want) {
			t.Fatalf("ingest pipeline missing %s", want)
		}
	}
	if strings.Contains(pipeline, "event.action = 'file.' + ctx.file.operation") {
		t.Fatal("ingest pipeline should preserve Beacon file actions instead of deriving lossy file operation actions")
	}
}

func TestFiles_NoError(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatalf("Files() returned unexpected error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("Files() returned no files")
	}
}

func TestFiles_ContainsAllRequiredNames(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, f := range files {
		names[f.Name] = true
	}
	for _, required := range []string{
		"README.md", "filebeat.yml", "elastic-agent-standalone.yml",
		"ilm-policy.json", "kibana-assets.ndjson", "docker-compose.yml",
		"sample-event.jsonl",
	} {
		if !names[required] {
			t.Errorf("Files() missing required file %q", required)
		}
	}
}

func TestFilesFromFS_PropagatesReadError(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := filesFromFS(emptyFS, DefaultLogPath)
	if err == nil {
		t.Fatal("filesFromFS with empty FS should return an error")
	}
	if !strings.Contains(err.Error(), "elastic pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestInputSnippetFromFS_ErrorOnMissingAsset(t *testing.T) {
	emptyFS := fstest.MapFS{}
	_, err := inputSnippetFromFS(emptyFS, "/some/path.jsonl")
	if err == nil {
		t.Fatal("inputSnippetFromFS with empty FS should return error")
	}
	if !strings.Contains(err.Error(), "elastic pack asset") {
		t.Fatalf("error should identify the pack source, got: %v", err)
	}
}

func TestInstallPack_ErrorOnWriteFailure(t *testing.T) {
	// A 0555 directory is only read-only where Unix permission bits exist; Windows ignores them.
	testenv.RequirePOSIXFileModes(t)
	if os.Getuid() == 0 {
		t.Skip("running as root: filesystem permission restrictions do not apply")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Skip("cannot set directory permissions:", err)
	}
	defer os.Chmod(dir, 0755)

	subdir := filepath.Join(dir, "output")
	err := InstallPack(subdir, DefaultLogPath)
	if err == nil {
		t.Fatal("InstallPack into read-only directory should return error")
	}
}

func TestInputSnippet_DefaultLogPath(t *testing.T) {
	got, err := InputSnippet("")
	if err != nil {
		t.Fatalf("InputSnippet with empty path returned error: %v", err)
	}
	if !strings.Contains(got, DefaultLogPath) {
		t.Fatalf("InputSnippet with empty logPath should use DefaultLogPath %q, got: %s", DefaultLogPath, got)
	}
}

// The native vcs block (session.commit_linked events) must not collide with the ECS vcs fields the
// pipeline derives from repository and branch: it is kept whole under beacon.vcs, and the ECS
// fields prefer its remote URL, branch and revision.
func TestIngestPipelineKeepsNativeVCS(t *testing.T) {
	pipeline := mustRead("pack/ingest-pipeline.json")
	for _, want := range []string{
		"ctx.remove('vcs')",
		"beacon.vcs = nativeVcs",
		"nativeVcs.repository.url.full",
		"'head').revision = vcsRevision",
	} {
		if !strings.Contains(pipeline, want) {
			t.Fatalf("ingest pipeline missing %s", want)
		}
	}
	// The native block is read before the ECS fields are written, or the first write would replace
	// the remote URL with the local path.
	if strings.Index(pipeline, "ctx.remove('vcs')") > strings.Index(pipeline, "ensure(ensure(ctx, 'vcs'), 'repository').url") {
		t.Fatal("native vcs must be captured before ECS vcs fields are set")
	}
	var mappings map[string]interface{}
	if err := json.Unmarshal([]byte(mustRead("pack/component-template-mappings.json")), &mappings); err != nil {
		t.Fatal(err)
	}
	props := mappings["template"].(map[string]interface{})["mappings"].(map[string]interface{})["properties"].(map[string]interface{})
	beacon := props["beacon"].(map[string]interface{})["properties"].(map[string]interface{})
	if _, ok := beacon["vcs"]; !ok {
		t.Fatal("beacon.vcs is not mapped")
	}
	sawLink := false
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/sample-event.jsonl")))
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"session.commit_linked"`) {
			sawLink = true
		}
	}
	if !sawLink {
		t.Fatal("sample events should include a session.commit_linked event")
	}
}

// prompt.id names the turn an event belongs to and is on events that carry no prompt text, so the
// pipeline copies it into beacon.prompt on its own -- not only alongside the text -- before the
// remove processor drops the source prompt object, and the template maps it as a keyword.
func TestIngestPipelineKeepsPromptID(t *testing.T) {
	pipeline := mustRead("pack/ingest-pipeline.json")
	for _, want := range []string{
		"if (ctx.prompt.id != null) { ensure(beacon, 'prompt').id = ctx.prompt.id; }",
		"if (ctx.prompt.text != null) { ensure(beacon, 'prompt').text = ctx.prompt.text; }",
	} {
		if !strings.Contains(pipeline, want) {
			t.Fatalf("ingest pipeline missing %s", want)
		}
	}
	if strings.Contains(pipeline, "ctx.prompt instanceof Map && ctx.prompt.text != null") {
		t.Fatal("prompt is copied only when it has text, which drops prompt.id from every other event of a turn")
	}
	var mappings map[string]interface{}
	if err := json.Unmarshal([]byte(mustRead("pack/component-template-mappings.json")), &mappings); err != nil {
		t.Fatal(err)
	}
	props := mappings["template"].(map[string]interface{})["mappings"].(map[string]interface{})["properties"].(map[string]interface{})
	beacon := props["beacon"].(map[string]interface{})["properties"].(map[string]interface{})
	prompt := beacon["prompt"].(map[string]interface{})["properties"].(map[string]interface{})
	if id, _ := prompt["id"].(map[string]interface{}); id["type"] != "keyword" {
		t.Fatalf("beacon.prompt.id mapping = %#v, want keyword", prompt["id"])
	}
}
