package ci

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Runners that start together can read the same clock value; the random half of the upload
// session keeps their keys apart. The random source here returns a new byte per call, so a
// finish that re-derived the session instead of reading the saved one would get a new key.
func TestSessionsOnTheSameClockTickGetTheirOwnKeys(t *testing.T) {
	start := time.Unix(1700000000, 10).UTC()
	oldClock, oldRandom := sessionClock, sessionRandom
	sessionClock = func() time.Time { return start }
	var calls byte
	sessionRandom = func(b []byte) (int, error) {
		calls++
		for i := range b {
			b[i] = calls
		}
		return len(b), nil
	}
	t.Cleanup(func() { sessionClock, sessionRandom = oldClock, oldRandom })
	t.Setenv(EnvS3Bucket, "bucket")
	t.Setenv(EnvS3Prefix, "logs")
	setGitHubRun(t)

	first := provisionUploadSession(t, UploadS3)
	second := provisionUploadSession(t, UploadS3)
	if got, want := first.Uploads[0].Key, "logs/fixture/repo/123/1/matrix/1700000000000000010-01010101/runtime.jsonl"; got != want {
		t.Fatalf("first key = %q, want %q", got, want)
	}
	if got, want := second.Uploads[0].Key, "logs/fixture/repo/123/1/matrix/1700000000000000010-02020202/runtime.jsonl"; got != want {
		t.Fatalf("second key = %q, want %q", got, want)
	}
	if first.StartedAt != "2023-11-14T22:13:20.00000001Z" {
		t.Fatalf("StartedAt = %q, want full precision", first.StartedAt)
	}

	state := filepath.Join(t.TempDir(), "state.json")
	if err := first.SaveState(state); err != nil {
		t.Fatal(err)
	}
	restored, err := LoadState(state)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := ResolveUploadDestinationsForSession([]string{UploadS3}, restored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(finish, first.Uploads) {
		t.Fatalf("finish uploads to %+v, start chose %+v", finish, first.Uploads)
	}
}
