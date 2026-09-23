package sentinel

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestDCRTransformMentionsExpectedColumns(t *testing.T) {
	got := DCRTransform()
	for _, want := range []string{
		"RawEvent = todynamic(RawData)",
		"TimeGenerated = coalesce(todatetime(RawEvent.timestamp), TimeGenerated)",
		"EventAction = tostring(RawEvent.event.action)",
		"CommandLine = coalesce(tostring(RawEvent.command.command), tostring(RawEvent.tool.command))",
		"RawData = RawEvent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("DCR transform missing %q: %s", want, got)
		}
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{
		"README.md",
		"dcr-transform.kql",
		"table-schema.json",
		"dcr-template.json",
		"queries.kql",
		"detections.kql",
		"sample-event.jsonl",
		"vector.toml",
		"dcr-logs-ingestion-template.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	dcrTemplate, err := os.ReadFile(filepath.Join(dir, "dcr-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dcrTemplate), "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("generated DCR template missing configured log path: %s", dcrTemplate)
	}
	if strings.Contains(string(dcrTemplate), "{{LOG_PATH}}") {
		t.Fatalf("generated DCR template still contains template token: %s", dcrTemplate)
	}
}

func TestInstallPackBackslashPathProducesValidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, `C:\Users\me\beacon\runtime.jsonl`); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	dcrTemplate, err := os.ReadFile(filepath.Join(dir, "dcr-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(dcrTemplate, &doc); err != nil {
		t.Fatalf("DCR template with backslash path is not valid JSON: %v\n%s", err, dcrTemplate)
	}
	if !strings.Contains(string(dcrTemplate), `C:\\Users\\me\\beacon\\runtime.jsonl`) {
		t.Fatalf("DCR template missing escaped backslash path: %s", dcrTemplate)
	}
}

func TestPackJSONFilesAreValid(t *testing.T) {
	for _, path := range []string{
		"pack/table-schema.json",
		"pack/dcr-template.json",
		"pack/dcr-logs-ingestion-template.json",
	} {
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(mustRead(path)), &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
	}
}

func TestRenderedDCRTemplateContainsTransformFromKQL(t *testing.T) {
	for _, path := range []string{"pack/dcr-template.json", "pack/dcr-logs-ingestion-template.json"} {
		rendered := renderDCRTemplate(path)
		if strings.Contains(rendered, "{{DCR_TRANSFORM}}") {
			t.Fatalf("%s still contains the {{DCR_TRANSFORM}} placeholder", path)
		}
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(rendered), &doc); err != nil {
			t.Fatalf("rendered %s is not valid JSON: %v", path, err)
		}
		transform := minifyKQL(DCRTransform())
		if !strings.Contains(rendered, transform) {
			t.Fatalf("rendered %s does not contain the minified dcr-transform.kql content", path)
		}
	}
}

// dcrRule is the part of a DCR ARM template the two ingestion paths have to agree on.
type dcrRule struct {
	Resources []struct {
		Kind       string `json:"kind"`
		Properties struct {
			StreamDeclarations map[string]struct {
				Columns []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"columns"`
			} `json:"streamDeclarations"`
			DataFlows []struct {
				Streams      []string `json:"streams"`
				OutputStream string   `json:"outputStream"`
			} `json:"dataFlows"`
		} `json:"properties"`
	} `json:"resources"`
}

func parseDCR(t *testing.T, path string) dcrRule {
	t.Helper()
	var rule dcrRule
	if err := json.Unmarshal([]byte(renderDCRTemplate(path)), &rule); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(rule.Resources) != 1 || len(rule.Resources[0].Properties.DataFlows) != 1 {
		t.Fatalf("%s: want one DCR with one data flow", path)
	}
	return rule
}

// The Logs Ingestion path only works if Vector posts to the stream the DCR declares, with the
// columns the shared transform reads, and the DCR writes to the same table as the Azure Monitor
// Agent path. Nothing in Azure checks any of that until the first record is rejected.
func TestLogsIngestionPathMatchesTheDCRAndTheAgentPath(t *testing.T) {
	vector := mustRead("pack/vector.toml.tmpl")
	ingest := parseDCR(t, "pack/dcr-logs-ingestion-template.json").Resources[0].Properties
	agent := parseDCR(t, "pack/dcr-template.json").Resources[0].Properties

	const stream = "Custom-BeaconRuntimeRaw"
	if !strings.Contains(vector, `stream_name = "`+stream+`"`) {
		t.Errorf("vector.toml does not post to %s", stream)
	}
	decl, ok := ingest.StreamDeclarations[stream]
	if !ok {
		t.Fatalf("the Logs Ingestion DCR does not declare %s", stream)
	}
	columns := map[string]string{}
	for _, c := range decl.Columns {
		columns[c.Name] = c.Type
	}
	if columns["TimeGenerated"] != "datetime" || columns["RawData"] != "string" {
		t.Errorf("%s columns = %v, want TimeGenerated datetime and RawData string", stream, columns)
	}
	if got := ingest.DataFlows[0].Streams; len(got) != 1 || got[0] != stream {
		t.Errorf("the Logs Ingestion data flow reads %v, want %s", got, stream)
	}
	// Microsoft's Logs Ingestion templates mark the DCR Direct. Without it Azure treats the rule as
	// an agent DCR, not one the Logs Ingestion API posts to.
	if kind := parseDCR(t, "pack/dcr-logs-ingestion-template.json").Resources[0].Kind; kind != "Direct" {
		t.Errorf("the Logs Ingestion DCR has kind %q, want Direct", kind)
	}
	if ingest.DataFlows[0].OutputStream != agent.DataFlows[0].OutputStream {
		t.Errorf("the two paths write to different tables: %q and %q",
			ingest.DataFlows[0].OutputStream, agent.DataFlows[0].OutputStream)
	}
	for _, want := range []string{`type = "azure_logs_ingestion"`, `"RawData": raw`, `timestamp_field = "TimeGenerated"`} {
		if !strings.Contains(vector, want) {
			t.Errorf("vector.toml is missing %s", want)
		}
	}
}

// Credentials come from the Vector service environment, never from the pack.
func TestVectorConfigTakesAzureCredentialsFromTheEnvironment(t *testing.T) {
	vector := mustRead("pack/vector.toml.tmpl")
	for _, key := range []string{"azure_tenant_id", "azure_client_id", "azure_client_secret", "endpoint", "dcr_immutable_id"} {
		found := false
		for _, line := range strings.Split(vector, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, key+" =") {
				continue
			}
			found = true
			if !strings.Contains(line, `"${`) {
				t.Errorf("vector.toml sets %s to a literal instead of an environment variable: %s", key, line)
			}
		}
		if !found {
			t.Errorf("vector.toml does not set %s", key)
		}
	}
}

func TestSampleEventsCoverValidationHookAndOTelShapes(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(mustRead("pack/sample-event.jsonl")))
	var sawValidation, sawHook, sawOTel bool
	for scanner.Scan() {
		var doc map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			t.Fatalf("sample-event.jsonl is not valid JSONL: %v", err)
		}
		if destination, ok := doc["destination"].(map[string]interface{}); ok && destination["type"] == "sentinel" {
			sawValidation = true
		}
		if _, ok := doc["tool"].(map[string]interface{}); ok {
			sawHook = true
		}
		if raw, ok := doc["raw"].(map[string]interface{}); ok && raw["otel_signal"] != nil {
			sawOTel = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawValidation || !sawHook || !sawOTel {
		t.Fatalf("sample events should include validation, hook-rich, and OTel shapes; validation=%t hook=%t otel=%t", sawValidation, sawHook, sawOTel)
	}
}

func TestPackREADMEMentionsSentinelSetupAndSecretBoundaries(t *testing.T) {
	readme := mustRead("pack/README.md")
	for _, want := range []string{
		"beacon endpoint sentinel validate",
		"Azure Monitor Agent",
		"Data Collection Rule",
		"BeaconRuntime_CL",
		"Microsoft Sentinel",
		"Content Handling",
		"/var/log/beacon-agent/runtime.jsonl",
		"not in Beacon endpoint configuration",
		"Vector and the Logs Ingestion API",
		"dcr-logs-ingestion-template.json",
		"Monitoring Metrics Publisher",
		"CEF and Syslog",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("pack README missing %q", want)
		}
	}
}

func TestKQLAssetsMentionSentinelTableAndValidation(t *testing.T) {
	for _, path := range []string{
		"pack/queries.kql",
		"pack/detections.kql",
		"pack/dcr-transform.kql",
	} {
		got := mustRead(path)
		if !strings.Contains(got, "BeaconRuntime_CL") && path != "pack/dcr-transform.kql" {
			t.Fatalf("%s should mention BeaconRuntime_CL", path)
		}
		if strings.Contains(got, "{{LOG_PATH}}") {
			t.Fatalf("%s still contains template token", path)
		}
	}
	if !strings.Contains(mustRead("pack/queries.kql"), "Beacon endpoint Sentinel validation event") {
		t.Fatal("queries.kql should include the Sentinel validation phrase")
	}
}

// The Logs Ingestion API rejects requests over 1 MB, and the azure_logs_ingestion sink batches up to
// 10 MB by default, so a busy flush would lose a whole batch to 413s.
func TestVectorBatchesFitTheLogsIngestionLimit(t *testing.T) {
	vector := mustRead("pack/vector.toml.tmpl")
	// \r? because Windows checkouts carry CRLF, and (?m)$ matches only before \n.
	match := regexp.MustCompile(`(?m)^max_bytes = (\d+)\r?$`).FindStringSubmatch(vector)
	if match == nil {
		t.Fatal("vector.toml does not cap batch.max_bytes, so the sink batches up to 10 MB")
	}
	if n, _ := strconv.Atoi(match[1]); n <= 0 || n > 1_000_000 {
		t.Fatalf("batch.max_bytes = %s, want at most 1 MB", match[1])
	}
}
