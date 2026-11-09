package objectkey

import (
	"reflect"
	"testing"
)

func TestCleanPartsKeepsValuesInsideTheirPrefix(t *testing.T) {
	for value, want := range map[string][]string{
		"":                     nil,
		"/logs/":               {"logs"},
		" team / ci ":          {"team", "ci"},
		"a/../b/./c":           {"a", "b", "c"},
		`win\path`:             {"win-path"},
		"owner/repo//nightly/": {"owner", "repo", "nightly"},
	} {
		if got := CleanParts(value); !reflect.DeepEqual(got, want) {
			t.Errorf("CleanParts(%q) = %#v, want %#v", value, got, want)
		}
	}
}

func TestCleanPartIsOneSegment(t *testing.T) {
	for _, value := range []string{".", "..", "  ..  ", "/", "../foo", "../../etc/passwd", "a/../b", "foo/bar", "/../../etc/passwd/"} {
		if got := CleanPart(value); got != "" {
			t.Errorf("CleanPart(%q) = %q, want empty", value, got)
		}
	}
	for value, want := range map[string]string{
		"runtime":  "runtime",
		"foo..bar": "foo..bar",
		`win\path`: "win-path",
		"  job-1 ": "job-1",
	} {
		if got := CleanPart(value); got != want {
			t.Errorf("CleanPart(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestEscapeNameKeepsSeparators(t *testing.T) {
	for name, want := range map[string]string{
		"runtime/date=2026-10-30/1-claude-run 1.jsonl.gz": "runtime/date=2026-10-30/1-claude-run%201.jsonl.gz",
		"a?b/c#d/%": "a%3Fb/c%23d/%25",
		"bucket":    "bucket",
	} {
		if got := EscapeName(name); got != want {
			t.Errorf("EscapeName(%q) = %q, want %q", name, got, want)
		}
	}
}
