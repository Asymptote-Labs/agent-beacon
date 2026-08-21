package endpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	beaconauth "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ingest"
)

func TestEndpointSourceBatchesValidEventsAndMalformedRejects(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	content := []byte("{\"event\":\"one\"}\nnot-json\n{\"event\":\"two\"}\n")
	if err := os.WriteFile(logPath, content, 0600); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{FileOffsets: map[string]int64{}}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 1 {
		t.Fatalf("len(batches) = %d, want 1", len(batches))
	}
	if got := len(batches[0].Events); got != 2 {
		t.Fatalf("events = %d, want 2", got)
	}
	if batches[0].Rejected != 1 {
		t.Fatalf("Rejected = %d, want 1", batches[0].Rejected)
	}
	if batches[0].Cursor.Offset != int64(len(content)) {
		t.Fatalf("Offset = %d, want %d", batches[0].Cursor.Offset, len(content))
	}
}

func TestUploadTreatsMissingLogDirectoryAsNoBatches(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "missing", "runtime.jsonl")
	statePath := filepath.Join(root, "upload-state.json")
	store := ingest.Store{Path: statePath}
	if err := store.Save(ingest.State{LastError: "stale error"}); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	res := ingest.Upload(context.Background(), ingest.Options{
		Settings: ingest.Settings{Enabled: true, Managed: true, SourceID: "source-1"},
		Store:    store,
		Creds:    &beaconauth.Credentials{Token: "token", UserID: "user"},
		Source:   source,
	})

	if res.Uploaded {
		t.Fatalf("missing log directory should not upload: %#v", res.State)
	}
	if res.State.LastError != "" {
		t.Fatalf("LastError = %q", res.State.LastError)
	}
	if stored := store.Load(); stored.LastError != "" {
		t.Fatalf("persisted LastError = %q", stored.LastError)
	}
}

func TestEndpointSourceHonorsSavedOffset(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	first := "{\"event\":\"one\"}\n"
	second := "{\"event\":\"two\"}\n"
	if err := os.WriteFile(logPath, []byte(first+second), 0600); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{FileOffsets: map[string]int64{logPath: int64(len(first))}}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 1 || len(batches[0].Events) != 1 {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	if string(batches[0].Events[0]) != "{\"event\":\"two\"}" {
		t.Fatalf("event = %s", batches[0].Events[0])
	}
}

func TestEndpointSourceAppliesLegacyActiveOffsetToFirstRotatedArchive(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	archivePath := logPath + ".1"
	alreadyUploaded := "{\"event\":\"already-uploaded\"}\n"
	archiveNew := "{\"event\":\"archive-new\"}\n"
	activeNew := "{\"event\":\"active\"}\n"
	if err := os.WriteFile(archivePath, []byte(alreadyUploaded+archiveNew), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(activeNew), 0600); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{
		FileOffsets: map[string]int64{logPath: int64(len(alreadyUploaded))},
	}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 2 {
		t.Fatalf("len(batches) = %d, want 2: %#v", len(batches), batches)
	}
	if batches[0].Cursor.LogPath != archivePath || batches[0].Cursor.Archive != "runtime.jsonl.1" {
		t.Fatalf("unexpected archive cursor: %#v", batches[0].Cursor)
	}
	if string(batches[0].Events[0]) != "{\"event\":\"archive-new\"}" {
		t.Fatalf("archive event = %s", batches[0].Events[0])
	}
	if batches[1].Cursor.LogPath != logPath || string(batches[1].Events[0]) != "{\"event\":\"active\"}" {
		t.Fatalf("unexpected active batch: %#v", batches[1])
	}
}

func TestEndpointSourceAppliesLegacyActiveOffsetWithUnrelatedFileIDs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	archivePath := logPath + ".1"
	alreadyUploaded := "{\"event\":\"already-uploaded\"}\n"
	archiveNew := "{\"event\":\"archive-new\"}\n"
	if err := os.WriteFile(archivePath, []byte(alreadyUploaded+archiveNew), 0600); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{
		FileOffsets: map[string]int64{logPath: int64(len(alreadyUploaded))},
		FileIDs:     map[string]string{filepath.Join(dir, "other.jsonl"): "dev:ino"},
	}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 1 || batches[0].Cursor.LogPath != archivePath {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	if string(batches[0].Events[0]) != "{\"event\":\"archive-new\"}" {
		t.Fatalf("archive event = %s", batches[0].Events[0])
	}
}

func TestEndpointSourceDoesNotApplyLegacyActiveOffsetWhenActiveFileIDConflicts(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	archivePath := logPath + ".1"
	alreadyUploaded := "{\"event\":\"already-uploaded\"}\n"
	archiveNew := "{\"event\":\"archive-new\"}\n"
	if err := os.WriteFile(archivePath, []byte(alreadyUploaded+archiveNew), 0600); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{
		FileOffsets: map[string]int64{logPath: int64(len(alreadyUploaded))},
		FileIDs:     map[string]string{logPath: "different-file"},
	}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 1 || batches[0].Cursor.LogPath != archivePath {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	if len(batches[0].Events) != 2 || string(batches[0].Events[0]) != "{\"event\":\"already-uploaded\"}" {
		t.Fatalf("archive batch should start at zero when active file identity conflicts: %#v", batches[0])
	}
}

func TestEndpointSourceReadsRotatedArchivesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	files := map[string]string{
		logPath + ".10": "{\"event\":\"oldest\"}\n",
		logPath + ".3":  "{\"event\":\"older\"}\n",
		logPath + ".2":  "{\"event\":\"middle\"}\n",
		logPath + ".1":  "{\"event\":\"newer\"}\n",
		logPath:         "{\"event\":\"active\"}\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{FileOffsets: map[string]int64{}}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 5 {
		t.Fatalf("len(batches) = %d, want 5: %#v", len(batches), batches)
	}
	wantArchives := []string{"runtime.jsonl.10", "runtime.jsonl.3", "runtime.jsonl.2", "runtime.jsonl.1", ""}
	wantEvents := []string{
		"{\"event\":\"oldest\"}",
		"{\"event\":\"older\"}",
		"{\"event\":\"middle\"}",
		"{\"event\":\"newer\"}",
		"{\"event\":\"active\"}",
	}
	for i := range batches {
		if batches[i].Cursor.Archive != wantArchives[i] {
			t.Fatalf("batch %d archive = %q, want %q", i, batches[i].Cursor.Archive, wantArchives[i])
		}
		if string(batches[i].Events[0]) != wantEvents[i] {
			t.Fatalf("batch %d event = %s, want %s", i, batches[i].Events[0], wantEvents[i])
		}
	}
}

func TestEndpointSourceIgnoresWriterLockFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	if err := os.WriteFile(logPath+".lock", []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("{\"event\":\"active\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{FileOffsets: map[string]int64{}}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 1 {
		t.Fatalf("len(batches) = %d, want 1: %#v", len(batches), batches)
	}
	if batches[0].Cursor.LogPath != logPath || batches[0].Cursor.Archive != "" {
		t.Fatalf("unexpected active cursor: %#v", batches[0].Cursor)
	}
	if batches[0].Rejected != 0 {
		t.Fatalf("Rejected = %d, want 0", batches[0].Rejected)
	}
	if string(batches[0].Events[0]) != "{\"event\":\"active\"}" {
		t.Fatalf("event = %s", batches[0].Events[0])
	}
}

func TestEndpointSourceFollowsSavedOffsetAcrossArchiveRenumbering(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	savedPath := logPath + ".1"
	renumberedPath := logPath + ".2"
	first := "{\"event\":\"one\"}\n"
	second := "{\"event\":\"two\"}\n"
	if err := os.WriteFile(renumberedPath, []byte(first+second), 0600); err != nil {
		t.Fatal(err)
	}

	// Identity is read from an open handle, not from a path: on Windows the file index is only
	// reachable that way, and a FileInfo carries no equivalent.
	handle, err := os.Open(renumberedPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	identity := fileIdentity(handle, info)
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if identity == "" {
		t.Fatal("file identity is empty, so this test cannot check that an offset follows a rotated file")
	}
	source := NewSource(endpointconfig.Config{UserMode: true, LogPath: logPath}, logPath, true)
	batches, err := source.Batches(ingest.State{
		FileOffsets: map[string]int64{savedPath: int64(len(first))},
		FileIDs:     map[string]string{savedPath: identity},
	}, 500, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if len(batches) != 1 || len(batches[0].Events) != 1 {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	if batches[0].Cursor.LogPath != renumberedPath || string(batches[0].Events[0]) != "{\"event\":\"two\"}" {
		t.Fatalf("unexpected renumbered archive batch: %#v", batches[0])
	}
	if batches[0].Cursor.FileID == "" {
		t.Fatalf("missing file identity on cursor: %#v", batches[0].Cursor)
	}
}
