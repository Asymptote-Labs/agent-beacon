package tomledit

import (
	"reflect"
	"testing"
)

func TestHeaderReadsEveryTOMLSpelling(t *testing.T) {
	for line, want := range map[string][]string{
		`[otel]`:                           {"otel"},
		`  [ otel . exporter ]  `:          {"otel", "exporter"},
		`[otel.exporter."otlp-grpc"]`:      {"otel", "exporter", "otlp-grpc"},
		`[mcp_servers.x] # mine`:           {"mcp_servers", "x"},
		`[mcp_servers."a#b"] # c`:          {"mcp_servers", "a#b"},
		`[mcp_servers.'a.b']`:              {"mcp_servers", "a.b"},
		`["mcp_servers"."beacon-managed"]`: {"mcp_servers", "beacon-managed"},
		`[mcp_servers."q\"]"]`:             {"mcp_servers", `q"]`},
		"[profiles.fast]\r":                {"profiles", "fast"},
		`[[hooks]] # Beacon`:               {"hooks"},
		`[[ "hooks" ]]`:                    {"hooks"},
	} {
		got, _, ok := Header(line)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("Header(%q) = %q, %v; want %q", line, got, ok, want)
		}
	}
	for _, line := range []string{
		`model = "o3"`,
		`args = ["a", "b"]`,
		`["a", "b"],`,
		`[1, 2]`,
		`[otel`,
		`[otel] trailing`,
		`[[hooks]`,
		`[hooks]]`,
		`[]`,
		`# [otel]`,
	} {
		if key, _, ok := Header(line); ok {
			t.Errorf("Header(%q) = %q, want no header", line, key)
		}
	}
	if _, array, _ := Header("[[hooks]]"); !array {
		t.Error("[[hooks]] not reported as an array of tables")
	}
	if _, array, _ := Header("[hooks]"); array {
		t.Error("[hooks] reported as an array of tables")
	}
}

func TestRemoveTableHandlesQuotingCommentsAndSubtables(t *testing.T) {
	doc := "a = 1\n\n[mcp_servers.\"beacon-managed\"] # Beacon\nurl = \"x\"\n\n[mcp_servers.beacon-managed.env]\nK = \"v\"\n\n[mcp_servers.beacon-managed-other] # mine\nurl = \"y\"\n"
	got, found := RemoveTable(doc, "mcp_servers", "beacon-managed")
	if !found {
		t.Fatal("not found")
	}
	want := "a = 1\n\n[mcp_servers.beacon-managed-other] # mine\nurl = \"y\"\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRemoveTableKeepsCRLFAndTrimsTrailingBlock(t *testing.T) {
	doc := "model = \"o3\"\r\n\r\n[otel] # telemetry\r\nenvironment = \"prod\"\r\n"
	got, found := RemoveTable(doc, "otel")
	if !found || got != "model = \"o3\"\r\n" {
		t.Fatalf("got %q, %v", got, found)
	}
	if _, found := RemoveTable("[otelx]\na = 1\n", "otel"); found {
		t.Fatal("[otelx] removed as [otel]")
	}
}
