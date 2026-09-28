package gitlink

import (
	"reflect"
	"strings"
	"testing"
)

func TestLinkLineRoundTrips(t *testing.T) {
	for _, link := range []Link{
		{Harness: "claude_code", SessionID: "0f8e2c1a-5b6d-4e7f-8a9b-0c1d2e3f4a5b"},
		{Harness: "codex", SessionID: "rollout-2026-09-27T10:00:00"},
		{Harness: "cursor", SessionID: "a/b"}, // only the first slash separates the harness
	} {
		got, ok := ParseLine(link.Line())
		if !ok || got != link {
			t.Fatalf("ParseLine(%q) = %+v, %v; want %+v", link.Line(), got, ok, link)
		}
	}
}

func TestParseLineRejectsWhatItCannotWriteBack(t *testing.T) {
	for _, line := range []string{
		"",
		"traces:abc",
		"beacon:",
		"beacon:claude_code",      // no session
		"beacon:claude_code/",     // empty session
		"beacon:Claude/abc",       // harness is lowercase snake case
		"beacon:claude-code/abc",  // ... with no dashes
		"beacon:/abc",             // empty harness
		"beacon:codex/ab\x1b[2Jc", // terminal escape in the id
		"beacon:codex/café",       // non-ASCII
		"beacon:codex/" + strings.Repeat("a", MaxSessionIDLength+1),
	} {
		if link, ok := ParseLine(line); ok {
			t.Errorf("ParseLine(%q) = %+v, want rejection", line, link)
		}
	}
}

func TestParseLineIgnoresTrailingFields(t *testing.T) {
	got, ok := ParseLine("  beacon:codex/abc future=field  ")
	if !ok || got != (Link{Harness: "codex", SessionID: "abc"}) {
		t.Fatalf("got %+v, %v", got, ok)
	}
}

func TestParseNoteDedupesInOrder(t *testing.T) {
	note := "beacon:codex/b\nsomeone's text\nbeacon:claude_code/a\n\nbeacon:codex/b\n"
	got := keys(ParseNote(note))
	want := []string{"codex/b", "claude_code/a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeNoteKeepsOtherLinesAndAppends(t *testing.T) {
	note := "Reviewed-by: a person\nbeacon:codex/a\nbeacon:v2 something newer\n"
	got := MergeNote(note, []Link{{"codex", "a"}, {"claude_code", "b"}, {"BAD", "c"}})
	want := "Reviewed-by: a person\nbeacon:codex/a\nbeacon:v2 something newer\nbeacon:claude_code/b\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestMergeNoteIsIdempotent(t *testing.T) {
	links := []Link{{"codex", "a"}, {"cursor", "b"}}
	once := MergeNote("", links)
	if twice := MergeNote(once, links); twice != once {
		t.Fatalf("second merge changed the note:\n%q\n%q", once, twice)
	}
	if once != "beacon:codex/a\nbeacon:cursor/b\n" {
		t.Fatalf("got %q", once)
	}
}

func TestMergeNoteNormalizesBlankLinesAndRepeats(t *testing.T) {
	// `git notes append` separates paragraphs with a blank line, and a note merged with the
	// cat_sort_uniq strategy can hold a link twice if one copy carries CRLF.
	got := MergeNote("beacon:codex/a\r\n\nbeacon:codex/a\n", nil)
	if got != "beacon:codex/a\n" {
		t.Fatalf("got %q", got)
	}
	if MergeNote("\n\n", nil) != "" {
		t.Fatal("an empty note must stay empty")
	}
}
