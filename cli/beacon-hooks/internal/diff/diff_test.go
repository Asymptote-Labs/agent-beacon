package diff

import (
	"strings"
	"testing"
)

func TestFromCursorEdits_SingleEdit(t *testing.T) {
	edits := []interface{}{
		map[string]interface{}{
			"old_string": "x = 1",
			"new_string": "x = 2",
		},
	}

	result := FromCursorEdits("/path/to/file.py", edits)

	if result == "" {
		t.Fatal("FromCursorEdits() returned empty string")
	}

	if !strings.Contains(result, "--- a/file.py") {
		t.Error("Missing --- header")
	}
	if !strings.Contains(result, "+++ b/file.py") {
		t.Error("Missing +++ header")
	}
	if !strings.Contains(result, "-x = 1") {
		t.Error("Missing old string in diff")
	}
	if !strings.Contains(result, "+x = 2") {
		t.Error("Missing new string in diff")
	}
}

func TestFromCursorEdits_MultipleEdits(t *testing.T) {
	edits := []interface{}{
		map[string]interface{}{
			"old_string": "x = 1",
			"new_string": "x = 2",
		},
		map[string]interface{}{
			"old_string": "y = 3",
			"new_string": "y = 4",
		},
	}

	result := FromCursorEdits("/path/to/file.py", edits)

	if result == "" {
		t.Fatal("FromCursorEdits() returned empty string")
	}

	// Should contain two diffs separated by double newline
	parts := strings.Split(result, "\n\n")
	if len(parts) < 2 {
		t.Errorf("Expected at least 2 diff sections, got %d", len(parts))
	}

	if !strings.Contains(result, "-x = 1") {
		t.Error("Missing first old string")
	}
	if !strings.Contains(result, "+x = 2") {
		t.Error("Missing first new string")
	}
	if !strings.Contains(result, "-y = 3") {
		t.Error("Missing second old string")
	}
	if !strings.Contains(result, "+y = 4") {
		t.Error("Missing second new string")
	}
}

func TestFromCursorEdits_EmptyEdits(t *testing.T) {
	result := FromCursorEdits("/path/to/file.py", []interface{}{})

	if result != "" {
		t.Errorf("FromCursorEdits() with empty edits = %q, want empty", result)
	}
}

func TestFromCursorEdits_InvalidEditFormat(t *testing.T) {
	edits := []interface{}{
		"not a map",
		42,
	}

	result := FromCursorEdits("/path/to/file.py", edits)

	if result != "" {
		t.Errorf("FromCursorEdits() with invalid edits = %q, want empty", result)
	}
}

func TestFromCursorEdits_EmptyStrings(t *testing.T) {
	edits := []interface{}{
		map[string]interface{}{
			"old_string": "",
			"new_string": "",
		},
	}

	result := FromCursorEdits("/path/to/file.py", edits)

	// fromEditTool returns "" for empty old and new strings
	if result != "" {
		t.Errorf("FromCursorEdits() with empty strings = %q, want empty", result)
	}
}

func TestFromCursorEdits_MultilineEdit(t *testing.T) {
	edits := []interface{}{
		map[string]interface{}{
			"old_string": "func main() {\n\tfmt.Println(\"hello\")\n}",
			"new_string": "func main() {\n\tfmt.Println(\"world\")\n\treturn\n}",
		},
	}

	result := FromCursorEdits("/path/to/main.go", edits)

	if result == "" {
		t.Fatal("FromCursorEdits() returned empty string")
	}

	if !strings.Contains(result, "--- a/main.go") {
		t.Error("Missing file header")
	}
}
