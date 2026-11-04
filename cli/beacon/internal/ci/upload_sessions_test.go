package ci

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// setGitHubRun gives every session in the test the same GitHub run, attempt and job, as the
// legs of one matrix job (or repeated sessions in one job) see them.
func setGitHubRun(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_REPOSITORY", "fixture/repo")
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	t.Setenv("GITHUB_JOB", "matrix")
}

func provisionUploadSession(t *testing.T, uploads ...string) *Session {
	t.Helper()
	t.Setenv("RUNNER_TEMP", t.TempDir())
	collector := fakeExecutable(t, "collector", "#!/bin/sh\nexit 0\n")
	oldResolve := resolveCollectorBinary
	resolveCollectorBinary = func(string) (string, error) { return collector, nil }
	t.Cleanup(func() { resolveCollectorBinary = oldResolve })
	session, err := Provision(Options{CollectorPath: collector, Harness: "claude", Uploads: uploads})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// Every runner of a matrix job owns a different runtime.jsonl. Run the real uploader through
// a local aws/gcloud stand-in that stores objects by key, and check no runner's upload
// replaces another's.
func TestMatrixRunnersInOneWorkflowRunKeepTheirOwnUploads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local CLI stand-in uses a POSIX shell")
	}
	store := t.TempDir()
	bin := t.TempDir()
	script := "#!/bin/sh\nset -eu\nkey=${4#*://bucket/}\nmkdir -p \"$BEACON_TEST_UPLOAD_STORE/$(dirname \"$key\")\"\ncp \"$3\" \"$BEACON_TEST_UPLOAD_STORE/$key\"\n"
	for _, name := range []string{"aws", "gcloud"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEACON_TEST_UPLOAD_STORE", store)
	t.Setenv(EnvS3Bucket, "bucket")
	t.Setenv(EnvGCSBucket, "bucket")
	t.Setenv(EnvGCSPrefix, "gcs")
	setGitHubRun(t)

	bodies := []string{"runner-one\n", "runner-two\n"}
	sessions := make([]*Session, len(bodies))
	for i, body := range bodies {
		sessions[i] = provisionUploadSession(t, UploadS3, UploadGCS)
		if err := os.WriteFile(sessions[i].LogPath, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions[i].UploadArtifacts(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for i, session := range sessions {
		for _, dest := range session.Uploads {
			got, err := os.ReadFile(filepath.Join(store, filepath.FromSlash(dest.Key)))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != bodies[i] {
				t.Errorf("%s object %q holds %q, want runner %d's %q", dest.Provider, dest.Key, got, i, bodies[i])
			}
		}
	}
}

// beacon ci finish runs in a later process from the saved state file, and must upload under
// the key beacon ci start chose.
func TestFinishUploadsUnderTheKeyStartChose(t *testing.T) {
	t.Setenv(EnvS3Bucket, "bucket")
	for name, setup := range map[string]func(*testing.T){
		"github": setGitHubRun,
		"other": func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", "")
			t.Setenv("CI", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			session := provisionUploadSession(t, UploadS3)
			state := filepath.Join(t.TempDir(), "state.json")
			if err := session.SaveState(state); err != nil {
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
			if !reflect.DeepEqual(finish, session.Uploads) {
				t.Fatalf("finish uploads to %+v, start chose %+v", finish, session.Uploads)
			}
		})
	}
}
