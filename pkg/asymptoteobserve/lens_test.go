package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// lensSpecDir locates spec/lenses by walking up from this file, so the tests read the files the
// spec publishes rather than copies of them.
func lensSpecDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		candidate := filepath.Join(dir, "spec", "lenses")
		if _, err := os.Stat(filepath.Join(candidate, "VERSION")); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate spec/lenses/VERSION")
		}
		dir = parent
	}
}

func validLensManifest() LensManifestV1 {
	return LensManifestV1{ID: "files-changed", Title: "Files Changed", Icon: "file", Version: 1, API: LensAPIVersion}
}

func TestLensManifestValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*LensManifestV1)
		want   string // substring of the error; "" means valid
	}{
		{"valid", func(*LensManifestV1) {}, ""},
		{"optional fields empty", func(m *LensManifestV1) { m.Icon, m.Description, m.Author = "", "", "" }, ""},
		{"id uppercase", func(m *LensManifestV1) { m.ID = "Files" }, "id"},
		{"id leading hyphen", func(m *LensManifestV1) { m.ID = "-files" }, "id"},
		{"id path traversal", func(m *LensManifestV1) { m.ID = "../files" }, "id"},
		{"id too long", func(m *LensManifestV1) { m.ID = strings.Repeat("a", 64) }, "id"},
		{"title missing", func(m *LensManifestV1) { m.Title = "  " }, "title is required"},
		{"title too long", func(m *LensManifestV1) { m.Title = strings.Repeat("x", 61) }, "title is longer"},
		{"title multi-line", func(m *LensManifestV1) { m.Title = "a\nb" }, "title must be one line"},
		{"description control char", func(m *LensManifestV1) { m.Description = "a\x1bb" }, "description must be one line"},
		{"description too long", func(m *LensManifestV1) { m.Description = strings.Repeat("x", 281) }, "description is longer"},
		{"author invalid utf8", func(m *LensManifestV1) { m.Author = "\xff" }, "author is not valid UTF-8"},
		{"icon with space", func(m *LensManifestV1) { m.Icon = "two words" }, "icon"},
		{"version zero", func(m *LensManifestV1) { m.Version = 0 }, "version"},
		{"api missing", func(m *LensManifestV1) { m.API = "" }, "api"},
		{"api future", func(m *LensManifestV1) { m.API = "beacon.lens.v2" }, "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validLensManifest()
			tc.mutate(&m)
			err := m.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestLensManifestValidateReportsEveryProblem(t *testing.T) {
	err := LensManifestV1{}.Validate()
	for _, want := range []string{"id", "title is required", "version", "api"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() = %v, want it to mention %q", err, want)
		}
	}
}

func TestParseLensManifest(t *testing.T) {
	open := `<script type="application/beacon-lens+json">`
	good := `{"id":"x","title":"X","version":1,"api":"beacon.lens.v1"}`
	cases := []struct {
		name    string
		html    string
		wantErr string
	}{
		{"ok", "<html><head>" + open + good + "</script></head></html>", ""},
		{"whitespace around json", open + "\n  " + good + "\n" + "</script>", ""},
		{"missing", "<html><script>var x = 1</script></html>", "has no"},
		{"other spelling is not a manifest", `<script type='application/beacon-lens+json'>` + good + "</script>", "has no"},
		{"duplicate", open + good + "</script>" + open + good + "</script>", "more than one manifest"},
		{"unclosed", open + good, "not closed"},
		{"unknown field", open + `{"id":"x","title":"X","version":1,"api":"beacon.lens.v1","permissions":["net"]}` + "</script>", "unknown field"},
		{"trailing value", open + good + good + "</script>", "more than one JSON value"},
		{"not json", open + "id: x" + "</script>", "lens manifest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseLensManifest([]byte(tc.html))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseLensManifest() error = %v", err)
				}
				if m.ID != "x" || m.Title != "X" || m.Version != 1 || m.API != LensAPIVersion {
					t.Fatalf("ParseLensManifest() = %+v", m)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseLensManifest() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
	if _, err := ParseLensManifest([]byte("<html></html>")); !errors.Is(err, ErrLensManifestMissing) {
		t.Fatalf("missing manifest error = %v, want ErrLensManifestMissing", err)
	}
}

func TestLensSpecVersionMatchesSpec(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(lensSpecDir(t), "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != LensSpecVersion {
		t.Fatalf("spec/lenses/VERSION = %q, LensSpecVersion = %q", got, LensSpecVersion)
	}
}

// TestLensExampleIsAValidLens keeps the published example lens installable and holds it to the
// spec's own rules, since agents copy it.
func TestLensExampleIsAValidLens(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(lensSpecDir(t), "examples", "activity.lens.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > LensMaxBytes {
		t.Fatalf("example lens is %d bytes, over LensMaxBytes", len(raw))
	}
	manifest, err := ParseLensManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "://", "fetch(", "localStorage"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Errorf("example lens uses %q, which the spec forbids", banned)
		}
	}
	if !bytes.Contains(raw, []byte("window.beacon.getTrace()")) {
		t.Error("example lens does not call window.beacon.getTrace()")
	}
}

// TestLensExampleDataMatchesContract pins the published LensDataV1 example to the Go types: every
// key in it must decode into a field, and re-encoding must reproduce it exactly. A renamed or
// dropped JSON tag fails here, and so does an example that drifts from the types.
func TestLensExampleDataMatchesContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(lensSpecDir(t), "examples", "lens-data.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var data LensDataV1
	if err := decoder.Decode(&data); err != nil {
		t.Fatalf("example does not decode strictly into LensDataV1: %v", err)
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var want, got interface{}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("example does not round-trip through LensDataV1:\nwant %s\ngot  %s", raw, encoded)
	}

	if data.APIVersion != LensAPIVersion {
		t.Errorf("api_version = %q, want %q", data.APIVersion, LensAPIVersion)
	}
	if data.Trace.SchemaVersion != TraceSchemaVersion {
		t.Errorf("trace.schema_version = %q, want %q", data.Trace.SchemaVersion, TraceSchemaVersion)
	}
	ids := map[string]bool{}
	for i, event := range data.Trace.Events {
		if event.ID == "" || event.Number != i+1 || event.Timestamp == "" || event.Type == "" || event.Action == "" {
			t.Errorf("event %d lacks a field the spec guarantees: %+v", i, event)
		}
		ids[event.ID] = true
	}
	if data.Findings == nil || len(data.Findings.Items) == 0 {
		t.Fatal("example should show a finding")
	}
	for _, finding := range data.Findings.Items {
		for _, id := range finding.EventIDs {
			if !ids[id] {
				t.Errorf("finding %s cites event %s, which is not in the example bundle", finding.RuleID, id)
			}
		}
	}
}

// TestLensManifestSchemaInSync guards the hand-maintained manifest.schema.json against the Go
// manifest and its validator.
func TestLensManifestSchemaInSync(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(lensSpecDir(t), "manifest.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		AdditionalProperties *bool    `json:"additionalProperties"`
		Required             []string `json:"required"`
		Properties           map[string]struct {
			Pattern   string `json:"pattern"`
			MaxLength int    `json:"maxLength"`
			Const     string `json:"const"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Error("schema must set additionalProperties: false, matching the strict decoder")
	}

	var fields, required []string
	manifestType := reflect.TypeOf(LensManifestV1{})
	for i := 0; i < manifestType.NumField(); i++ {
		tag := manifestType.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		fields = append(fields, name)
		if opts != "omitempty" {
			required = append(required, name)
		}
	}
	var schemaFields []string
	for name := range schema.Properties {
		schemaFields = append(schemaFields, name)
	}
	sort.Strings(fields)
	sort.Strings(required)
	sort.Strings(schemaFields)
	schemaRequired := append([]string(nil), schema.Required...)
	sort.Strings(schemaRequired)
	if !reflect.DeepEqual(fields, schemaFields) {
		t.Errorf("schema properties %v, Go fields %v", schemaFields, fields)
	}
	if !reflect.DeepEqual(required, schemaRequired) {
		t.Errorf("schema required %v, Go required %v", schemaRequired, required)
	}

	props := schema.Properties
	if props["id"].Pattern != lensIDPattern.String() {
		t.Errorf("id pattern: schema %q, Go %q", props["id"].Pattern, lensIDPattern.String())
	}
	if props["icon"].Pattern != lensIconPattern.String() {
		t.Errorf("icon pattern: schema %q, Go %q", props["icon"].Pattern, lensIconPattern.String())
	}
	for field, max := range map[string]int{"title": lensTitleMaxRunes, "description": lensDescriptionMaxRunes, "author": lensAuthorMaxRunes} {
		if props[field].MaxLength != max {
			t.Errorf("%s maxLength: schema %d, Go %d", field, props[field].MaxLength, max)
		}
	}
	if props["api"].Const != LensAPIVersion {
		t.Errorf("api const: schema %q, Go %q", props["api"].Const, LensAPIVersion)
	}
}
