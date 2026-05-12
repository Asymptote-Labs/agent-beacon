package diff

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FromToolResponse builds a diff from any supported tool type
func FromToolResponse(toolName string, toolInput, toolResponse map[string]interface{}) string {
	filePath := GetStringFromMaps("file_path", toolInput, toolResponse)
	if filePath == "" {
		filePath = GetStringFromMaps("filePath", toolInput, toolResponse)
	}
	if filePath == "" {
		return ""
	}

	// Prefer structuredPatch if available (most accurate)
	if structuredPatch, ok := toolResponse["structuredPatch"].([]interface{}); ok && len(structuredPatch) > 0 {
		return fromStructuredPatch(filePath, structuredPatch)
	}

	// Fall back to tool-specific construction
	switch toolName {
	case "Edit":
		oldString := resolveEditString("old_string", "oldString", "old_str", toolInput, toolResponse)
		newString := resolveEditString("new_string", "newString", "new_str", toolInput, toolResponse)
		return fromEditTool(filePath, oldString, newString)

	case "copilot_insertEdit":
		// Copilot sends full file content as "code" field
		content := GetStringFromMaps("code", toolInput, toolResponse)
		return fromWriteTool(filePath, content, "")

	case "Write", "Create":
		content := GetStringFromMaps("content", toolInput, toolResponse)
		originalFile := GetStringFromMaps("originalFile", toolInput, toolResponse)
		return fromWriteTool(filePath, content, originalFile)

	case "MultiEdit":
		edits, ok := toolInput["edits"].([]interface{})
		if !ok || len(edits) == 0 {
			return ""
		}

		var allDiffs []string
		for _, edit := range edits {
			editMap, ok := edit.(map[string]interface{})
			if !ok {
				continue
			}
			editFile := GetStringFromMaps("file_path", editMap)
			if editFile == "" {
				editFile = filePath
			}
			oldStr := resolveEditString("old_string", "oldString", "old_str", editMap)
			newStr := resolveEditString("new_string", "newString", "new_str", editMap)
			if d := fromEditTool(editFile, oldStr, newStr); d != "" {
				allDiffs = append(allDiffs, d)
			}
		}
		return strings.Join(allDiffs, "\n\n")

	case "create_file", "copilot_createFile":
		content := GetStringFromMaps("content", toolInput, toolResponse)
		return fromWriteTool(filePath, content, "")

	case "apply_patch", "copilot_applyPatch":
		patchInput := GetStringFromMaps("input", toolInput, toolResponse)
		if patchInput == "" {
			patchInput = GetStringFromMaps("textResultForLlm", toolInput, toolResponse)
		}
		if patchInput != "" {
			return fromCopilotPatch(filePath, patchInput)
		}
		return ""
	}

	return ""
}

// resolveEditString tries multiple key variants for edit fields.
// Claude uses old_string/new_string, Copilot uses oldString/newString, Factory uses old_str/new_str.
func resolveEditString(snakeKey, camelKey, shortKey string, maps ...map[string]interface{}) string {
	if v := GetStringFromMaps(snakeKey, maps...); v != "" {
		return v
	}
	if v := GetStringFromMaps(camelKey, maps...); v != "" {
		return v
	}
	return GetStringFromMaps(shortKey, maps...)
}

// fromCopilotPatch parses Copilot's patch format and converts to unified diff.
// Format:
//
//	*** Begin Patch
//	*** Update File: path/to/file.py
//	@@@ -10,3 +10,5 @@@
//	 context line
//	-removed line
//	+added line
//	*** End Patch
func fromCopilotPatch(filePath, patchInput string) string {
	lines := strings.Split(patchInput, "\n")
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip patch boundaries
		if trimmed == "*** Begin Patch" || trimmed == "*** End Patch" {
			continue
		}

		// File headers: extract filename and emit appropriate diff header
		if oldPath, newPath, ok := parsePatchFileHeader(trimmed, filePath); ok {
			result = append(result, oldPath, newPath)
			continue
		}

		// Convert @@@ markers to standard @@ markers
		if strings.HasPrefix(trimmed, "@@@") {
			result = append(result, strings.Replace(line, "@@@", "@@", 2))
			continue
		}

		// Pass through diff content lines (context, additions, removals)
		if len(result) > 0 {
			result = append(result, line)
		}
	}

	if len(result) == 0 {
		filename := filepath.Base(filePath)
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n%s", filename, filename, patchInput)
	}

	return strings.Join(result, "\n")
}

// parsePatchFileHeader parses a Copilot patch file header line and returns the
// diff header lines. Returns ok=true if the line was a file header.
func parsePatchFileHeader(trimmed, fallbackPath string) (oldPath, newPath string, ok bool) {
	type headerDef struct {
		prefix string
		oldFmt string
		newFmt string
	}

	headers := []headerDef{
		{"*** Update File:", "--- a/%s", "+++ b/%s"},
		{"*** Add File:", "--- /dev/null", "+++ b/%s"},
		{"*** Delete File:", "--- a/%s", "+++ /dev/null"},
	}

	for _, h := range headers {
		if !strings.HasPrefix(trimmed, h.prefix) {
			continue
		}
		file := strings.TrimSpace(strings.TrimPrefix(trimmed, h.prefix))
		if file == "" {
			file = fallbackPath
		}
		name := filepath.Base(file)

		old := h.oldFmt
		if strings.Contains(old, "%s") {
			old = fmt.Sprintf(old, name)
		}
		newP := h.newFmt
		if strings.Contains(newP, "%s") {
			newP = fmt.Sprintf(newP, name)
		}
		return old, newP, true
	}
	return "", "", false
}

func fromStructuredPatch(filePath string, structuredPatch []interface{}) string {
	if len(structuredPatch) == 0 {
		return ""
	}

	filename := filepath.Base(filePath)
	lines := []string{
		fmt.Sprintf("--- a/%s", filename),
		fmt.Sprintf("+++ b/%s", filename),
	}

	for _, hunk := range structuredPatch {
		hunkMap, ok := hunk.(map[string]interface{})
		if !ok {
			continue
		}

		oldStart := getIntFromMap(hunkMap, "oldStart", 1)
		oldLines := getIntFromMap(hunkMap, "oldLines", 0)
		newStart := getIntFromMap(hunkMap, "newStart", 1)
		newLines := getIntFromMap(hunkMap, "newLines", 0)

		lines = append(lines, fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldLines, newStart, newLines))

		if hunkLines, ok := hunkMap["lines"].([]interface{}); ok {
			for _, line := range hunkLines {
				if lineStr, ok := line.(string); ok {
					lines = append(lines, lineStr)
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

// FromCursorEdits builds a unified diff from Cursor's afterFileEdit edits array.
// Each edit has old_string and new_string fields, reusing the existing fromEditTool helper.
func FromCursorEdits(filePath string, edits []interface{}) string {
	var allDiffs []string
	for _, edit := range edits {
		editMap, ok := edit.(map[string]interface{})
		if !ok {
			continue
		}
		oldStr, _ := editMap["old_string"].(string)
		newStr, _ := editMap["new_string"].(string)
		if d := fromEditTool(filePath, oldStr, newStr); d != "" {
			allDiffs = append(allDiffs, d)
		}
	}
	return strings.Join(allDiffs, "\n\n")
}

func fromEditTool(filePath, oldString, newString string) string {
	if oldString == "" && newString == "" {
		return ""
	}

	filename := filepath.Base(filePath)
	oldLines := splitLines(oldString)
	newLines := splitLines(newString)

	lines := []string{
		fmt.Sprintf("--- a/%s", filename),
		fmt.Sprintf("+++ b/%s", filename),
		fmt.Sprintf("@@ -1,%d +1,%d @@", len(oldLines), len(newLines)),
	}

	for _, line := range oldLines {
		lines = append(lines, "-"+line)
	}
	for _, line := range newLines {
		lines = append(lines, "+"+line)
	}

	return strings.Join(lines, "\n")
}

func fromWriteTool(filePath, content, originalFile string) string {
	filename := filepath.Base(filePath)
	newLines := splitLines(content)
	oldLines := splitLines(originalFile)

	lines := []string{
		fmt.Sprintf("--- a/%s", filename),
		fmt.Sprintf("+++ b/%s", filename),
	}

	if originalFile != "" {
		// This is an update - show as replacement
		lines = append(lines, fmt.Sprintf("@@ -1,%d +1,%d @@", len(oldLines), len(newLines)))
		for _, line := range oldLines {
			lines = append(lines, "-"+line)
		}
		for _, line := range newLines {
			lines = append(lines, "+"+line)
		}
	} else {
		// This is a new file
		lines = append(lines, fmt.Sprintf("@@ -0,0 +1,%d @@", len(newLines)))
		for _, line := range newLines {
			lines = append(lines, "+"+line)
		}
	}

	return strings.Join(lines, "\n")
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// GetStringFromMaps returns the first non-empty string value for key across the given maps.
func GetStringFromMaps(key string, maps ...map[string]interface{}) string {
	for _, m := range maps {
		if m == nil {
			continue
		}
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func getIntFromMap(m map[string]interface{}, key string, defaultVal int) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return defaultVal
}
