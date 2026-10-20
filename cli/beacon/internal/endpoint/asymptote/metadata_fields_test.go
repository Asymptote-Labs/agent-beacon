package asymptote

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// metadataOnlyRemoved is every event field Metadata-only forwarding removes: free text, documents,
// arguments, schemas and messages that a person, a model or a server wrote. Each one must have its
// own del(event.<path>) in the beacon_runtime_metadata transform. A path removes everything under it.
var metadataOnlyRemoved = []string{
	"prompt.text",
	"message",
	"raw",
	"command.output",
	"file.diff",
	"gen_ai.input",
	"gen_ai.output",
	"gen_ai.system_instructions",
	"gen_ai.tool.description",
	"gen_ai.tool.definitions",
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
	"gen_ai.retrieval",
	"gen_ai.evaluation.explanation",
	"gen_ai.agent.description",
	"gen_ai.request.stop_sequences",
	"approval.reason",
	"policy.reason",
}

// metadataOnlyKept is every text-shaped event field (string, list, map or untyped value) that
// Metadata-only forwarding keeps: identifiers, names, enums, versions, paths, the command line and
// other structured metadata the docs promise. Numbers and booleans are metadata by nature and are not
// listed. A new text-shaped field must be added here or to metadataOnlyRemoved, so that whoever adds
// it decides whether it is content.
var metadataOnlyKept = []string{
	"timestamp", "vendor", "product", "schema_version", "severity", "origin",
	"event.id", "event.kind", "event.action", "event.category", "event.fidelity",
	"endpoint.hostname", "endpoint.os", "endpoint.agent_version",
	"user.name", "user.uid",
	"harness.name", "harness.version", "harness.executable_path", "harness.config_path", "harness.collection_method",
	"error.type",
	"run.provider", "run.run_id", "run.run_attempt", "run.workflow", "run.job", "run.event_name",
	"run.commit", "run.repository", "run.branch", "run.pr", "run.pr_number", "run.actor",
	"session.id", "session.working_directory",
	"handoff.source_harness", "handoff.source_session_id",
	"share.visibility",
	"trace.id", "trace.span_id", "trace.parent_span_id",
	"jsonrpc.request.id", "jsonrpc.protocol.version",
	"network.protocol.name", "network.protocol.version", "network.transport",
	"rpc.response.status_code",
	"server.address",
	// The command line is kept: the docs list commands among what Metadata-only keeps.
	"tool.name", "tool.command", "tool.path",
	"file.path", "file.operation", "file.language", "file.diff_hash",
	"command.command",
	"mcp.server", "mcp.tool", "mcp.method.name", "mcp.protocol.version", "mcp.resource.uri", "mcp.session.id",
	"approval.decision",
	"policy.id", "policy.name", "policy.decision", "policy.enforcement",
	"prompt.id",
	"system_context.kind", "system_context.source",
	"system_context.skills.skill_name", "system_context.skills.skill_name_hash",
	"content.retention", "content.hash",
	"destination.type", "destination.mode", "destination.status",
	// Beacon's own component health; the reason is Beacon-authored, not agent content.
	"health.component", "health.status", "health.reason",
	"user_agent.name", "user_agent.version",
	"vcs.ref.head.revision", "vcs.ref.head.name", "vcs.ref.head.type",
	"vcs.repository.url.full", "vcs.attribution.method",
	// Beacon's own git hook timing record: the hook's name and the step it was in.
	"vcs.hook.name", "vcs.hook.phase",
	"gen_ai.agent.id", "gen_ai.agent.name", "gen_ai.agent.version",
	"gen_ai.conversation.id",
	"gen_ai.data_source.id",
	"gen_ai.evaluation.name", "gen_ai.evaluation.score.label",
	"gen_ai.operation.name",
	"gen_ai.prompt.name",
	"gen_ai.provider.name",
	"gen_ai.request.encoding_formats", "gen_ai.request.model",
	"gen_ai.response.finish_reasons", "gen_ai.response.id", "gen_ai.response.model",
	"gen_ai.token.type",
	"gen_ai.tool.call.id", "gen_ai.tool.name", "gen_ai.tool.type",
	"gen_ai.workflow.name",
	"model", "repository", "branch",
}

// eventLeaf is one JSON field of the event schema. Text is true for the shapes that can carry
// content (string, list, map, untyped value); numbers and booleans are not.
type eventLeaf struct {
	Path string
	Kind reflect.Kind
	Text bool
}

func eventLeaves(t reflect.Type, prefix string) []eventLeaf {
	var leaves []eventLeaf
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := field.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			leaves = append(leaves, eventLeaves(ft, path)...)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			leaves = append(leaves, eventLeaves(ft.Elem(), path)...)
		default:
			switch ft.Kind() {
			case reflect.String, reflect.Interface, reflect.Slice, reflect.Map:
				leaves = append(leaves, eventLeaf{Path: path, Kind: ft.Kind(), Text: true})
			default:
				leaves = append(leaves, eventLeaf{Path: path, Kind: ft.Kind()})
			}
		}
	}
	return leaves
}

func removedByMetadataOnly(path string) bool {
	for _, removed := range metadataOnlyRemoved {
		if path == removed || strings.HasPrefix(path, removed+".") {
			return true
		}
	}
	return false
}

func runtimeMetadataTransformSource(t *testing.T) string {
	t.Helper()
	var transforms struct {
		Transforms map[string]struct {
			Source string `toml:"source"`
		} `toml:"transforms"`
	}
	data, err := fs.ReadFile(packFS, privacyTransformAsset)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &transforms); err != nil {
		t.Fatal(err)
	}
	source := transforms.Transforms["beacon_runtime_metadata"].Source
	if source == "" {
		t.Fatal("beacon_runtime_metadata transform has no source")
	}
	return source
}

// Every content field has its own del() in the Metadata-only transform.
func TestMetadataTransformDeletesEveryContentField(t *testing.T) {
	source := runtimeMetadataTransformSource(t)
	for _, path := range metadataOnlyRemoved {
		if !strings.Contains(source, "del(event."+path+")\n") {
			t.Errorf("beacon_runtime_metadata does not remove %s: add del(event.%s)", path, path)
		}
	}
}

// Every text-shaped field of the event schema is classified as content (removed) or metadata
// (kept). A new field fails here until someone decides which it is.
func TestMetadataOnlyClassifiesEveryEventField(t *testing.T) {
	leaves := eventLeaves(reflect.TypeOf(schema.Event{}), "")
	known := map[string]bool{}
	for _, leaf := range leaves {
		known[leaf.Path] = true
		if !leaf.Text {
			continue
		}
		removed, kept := removedByMetadataOnly(leaf.Path), contains(metadataOnlyKept, leaf.Path)
		switch {
		case removed && kept:
			t.Errorf("%s is listed as both removed and kept by Metadata-only forwarding", leaf.Path)
		case !removed && !kept:
			t.Errorf("event field %s (%s) is not classified: add it to metadataOnlyRemoved if it can carry content a person, model or server wrote, or to metadataOnlyKept if it is metadata", leaf.Path, leaf.Kind)
		}
	}
	// Both lists name only fields that exist, so a renamed field cannot leave a stale entry behind.
	exists := func(path string) bool {
		for p := range known {
			if p == path || strings.HasPrefix(p, path+".") {
				return true
			}
		}
		return false
	}
	for _, path := range append(append([]string{}, metadataOnlyRemoved...), metadataOnlyKept...) {
		if !exists(path) {
			t.Errorf("%s is classified but is not an event field", path)
		}
	}
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// sentinelEvent builds one event with every field of the schema set, each text value naming its own
// path, so the output of the transform shows exactly which fields survived.
func sentinelEvent(t reflect.Type, prefix string) map[string]interface{} {
	out := map[string]interface{}{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := field.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		sentinel := metadataSentinel(path)
		switch ft.Kind() {
		case reflect.Struct:
			out[name] = sentinelEvent(ft, path)
		case reflect.Slice:
			if ft.Elem().Kind() == reflect.Struct {
				out[name] = []interface{}{sentinelEvent(ft.Elem(), path)}
			} else {
				out[name] = []interface{}{sentinel}
			}
		case reflect.Map, reflect.Interface:
			out[name] = map[string]interface{}{"text": sentinel}
		case reflect.String:
			out[name] = sentinel
		case reflect.Bool:
			out[name] = true
		default:
			out[name] = 1
		}
	}
	return out
}

func metadataSentinel(path string) string { return "SENTINEL:" + path + ":END" }

// metadataSentinelLeaks lists the content fields whose sentinel is still in an event's JSON and the
// metadata fields whose sentinel is gone.
func metadataSentinelLeaks(transformed string) (leaked, missing []string) {
	for _, leaf := range eventLeaves(reflect.TypeOf(schema.Event{}), "") {
		if !leaf.Text || leaf.Path == "content.retention" {
			continue // Metadata-only rewrites retention to "metadata"
		}
		present := strings.Contains(transformed, metadataSentinel(leaf.Path))
		if removedByMetadataOnly(leaf.Path) && present {
			leaked = append(leaked, leaf.Path)
		}
		if !removedByMetadataOnly(leaf.Path) && !present {
			missing = append(missing, leaf.Path)
		}
	}
	sort.Strings(leaked)
	sort.Strings(missing)
	return leaked, missing
}

// StripToMetadata is the Go twin of the Metadata-only transform, used where Beacon publishes an
// endpoint's events itself (`beacon share`). On an event with every field set, it removes exactly
// the fields the transform removes and keeps every other one.
func TestStripToMetadataRemovesExactlyWhatTheTransformRemoves(t *testing.T) {
	data, err := json.Marshal(sentinelEvent(reflect.TypeOf(schema.Event{}), ""))
	if err != nil {
		t.Fatal(err)
	}
	var event schema.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	asymptoteobserve.StripToMetadata(&event)
	stripped, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	leaked, missing := metadataSentinelLeaks(string(stripped))
	if len(leaked) > 0 {
		t.Errorf("StripToMetadata kept content fields %v: add each to StripToMetadata along with its del() in the transform\n%s", leaked, stripped)
	}
	if len(missing) > 0 {
		t.Errorf("StripToMetadata removed metadata fields %v:\n%s", missing, stripped)
	}
	if event.Content == nil || event.Content.Retention != schema.ContentRetentionMetadata || event.Content.Included || !event.Content.Redacted {
		t.Errorf("content marker = %+v, want metadata retention", event.Content)
	}
}

// Runs the Metadata-only transform with Vector's own VRL runtime on an event with every field set,
// and checks that every content field is gone and every metadata field is still there.
func TestMetadataTransformRemovesEveryContentFieldWithVector(t *testing.T) {
	vector := os.Getenv("BEACON_TEST_VECTOR_BIN")
	if vector == "" {
		t.Skip("set BEACON_TEST_VECTOR_BIN to run the metadata transform with Vector")
	}
	dir := t.TempDir()
	program := filepath.Join(dir, "runtime.vrl")
	if err := os.WriteFile(program, []byte(runtimeMetadataTransformSource(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	event, err := json.Marshal(sentinelEvent(reflect.TypeOf(schema.Event{}), ""))
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := json.Marshal(map[string]string{"message": string(event)})
	inputPath := filepath.Join(dir, "input.jsonl")
	if err := os.WriteFile(inputPath, append(wrapped, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(vector, "vrl", "--input", inputPath, "--program", program, "--print-object").CombinedOutput()
	if err != nil {
		t.Fatalf("vector vrl: %v\n%s", err, out)
	}
	var transformed string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var result struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &result) == nil && result.Message != "" {
			transformed = result.Message
		}
	}
	if transformed == "" {
		t.Fatalf("vector vrl produced no event:\n%s", out)
	}
	var parsed schema.Event
	if err := json.Unmarshal([]byte(transformed), &parsed); err != nil {
		t.Fatalf("transformed event is not an event: %v\n%s", err, transformed)
	}
	leaked, missing := metadataSentinelLeaks(transformed)
	if len(leaked) > 0 {
		t.Errorf("Metadata-only forwarded content fields %v:\n%s", leaked, transformed)
	}
	if len(missing) > 0 {
		t.Errorf("Metadata-only removed metadata fields %v:\n%s", missing, transformed)
	}
	if parsed.Content == nil || parsed.Content.Retention != schema.ContentRetentionMetadata || parsed.Content.Included {
		t.Errorf("content marker = %+v, want metadata retention", parsed.Content)
	}
}
