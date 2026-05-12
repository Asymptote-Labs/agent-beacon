package logging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSessionLogs_FileNotExist(t *testing.T) {
	data, offset, err := ReadSessionLogs("/nonexistent/file.log", 0)
	if err != nil {
		t.Fatalf("Expected no error for missing file, got %v", err)
	}
	if data != nil {
		t.Errorf("Expected nil data, got %d bytes", len(data))
	}
	if offset != 0 {
		t.Errorf("Expected offset 0, got %d", offset)
	}
}

func TestReadSessionLogs_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")
	os.WriteFile(logFile, []byte{}, 0644)

	data, offset, err := ReadSessionLogs(logFile, 0)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("Expected nil data for empty file, got %d bytes", len(data))
	}
	if offset != 0 {
		t.Errorf("Expected offset 0, got %d", offset)
	}
}

func TestReadSessionLogs_FullRead(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")
	content := `{"level":"info","message":"line1"}
{"level":"info","message":"line2"}
`
	os.WriteFile(logFile, []byte(content), 0644)

	data, offset, err := ReadSessionLogs(logFile, 0)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != content {
		t.Errorf("Data mismatch.\nGot:  %q\nWant: %q", string(data), content)
	}
	if offset != int64(len(content)) {
		t.Errorf("Expected offset %d, got %d", len(content), offset)
	}
}

func TestReadSessionLogs_DeltaRead(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	line1 := `{"level":"info","message":"line1"}` + "\n"
	line2 := `{"level":"info","message":"line2"}` + "\n"
	os.WriteFile(logFile, []byte(line1+line2), 0644)

	// Read from offset after line1 — should only get line2
	startOffset := int64(len(line1))
	data, offset, err := ReadSessionLogs(logFile, startOffset)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != line2 {
		t.Errorf("Data mismatch.\nGot:  %q\nWant: %q", string(data), line2)
	}
	if offset != int64(len(line1)+len(line2)) {
		t.Errorf("Expected offset %d, got %d", len(line1)+len(line2), offset)
	}
}

func TestReadSessionLogs_OffsetAtEOF(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")
	content := "some data\n"
	os.WriteFile(logFile, []byte(content), 0644)

	// Offset equals file size — nothing new
	data, offset, err := ReadSessionLogs(logFile, int64(len(content)))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("Expected nil data at EOF, got %d bytes", len(data))
	}
	if offset != int64(len(content)) {
		t.Errorf("Expected offset %d, got %d", len(content), offset)
	}
}

func TestReadSessionLogs_OffsetBeyondFileSize(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")
	content := "short"
	os.WriteFile(logFile, []byte(content), 0644)

	// Offset past file size (e.g. file was truncated) — should reset
	data, offset, err := ReadSessionLogs(logFile, 9999)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("Expected nil data, got %d bytes", len(data))
	}
	if offset != int64(len(content)) {
		t.Errorf("Expected offset reset to file size %d, got %d", len(content), offset)
	}
}

func TestReadSessionLogs_IncrementalAppend(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	// Write first chunk
	chunk1 := `{"msg":"first"}` + "\n"
	os.WriteFile(logFile, []byte(chunk1), 0644)

	// Read first chunk
	data, offset, err := ReadSessionLogs(logFile, 0)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != chunk1 {
		t.Errorf("First read: got %q, want %q", string(data), chunk1)
	}

	// Append second chunk
	chunk2 := `{"msg":"second"}` + "\n"
	f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(chunk2)
	f.Close()

	// Read from saved offset — should only get chunk2
	data, newOffset, err := ReadSessionLogs(logFile, offset)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != chunk2 {
		t.Errorf("Delta read: got %q, want %q", string(data), chunk2)
	}
	if newOffset != int64(len(chunk1)+len(chunk2)) {
		t.Errorf("Expected final offset %d, got %d", len(chunk1)+len(chunk2), newOffset)
	}
}
