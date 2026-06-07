package ci

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestNormalizeUploadsSplitsAndDedupes(t *testing.T) {
	got, err := normalizeUploads([]string{"s3,gcs", "s3"})
	if err != nil {
		t.Fatalf("normalizeUploads returned error: %v", err)
	}
	if want := []string{UploadS3, UploadGCS}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeUploads = %#v, want %#v", got, want)
	}
}

func TestNormalizeUploadsRejectsUnsupportedProvider(t *testing.T) {
	if _, err := normalizeUploads([]string{"elastic"}); err == nil || !strings.Contains(err.Error(), "unsupported --upload") {
		t.Fatalf("normalizeUploads error = %v, want unsupported provider", err)
	}
}

func TestResolveUploadDestinationsRequiresBucket(t *testing.T) {
	t.Setenv(EnvS3Bucket, "")
	_, err := resolveUploadDestinations([]string{"s3"}, nil, "/tmp/runtime.jsonl", time.Unix(1700000000, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), EnvS3Bucket) {
		t.Fatalf("resolveUploadDestinations error = %v, want missing bucket", err)
	}
}

func TestResolveUploadDestinationsBuildsGitHubObjectKeys(t *testing.T) {
	t.Setenv(EnvS3Bucket, "beacon-ci")
	t.Setenv(EnvS3Prefix, "logs/")
	run := &schema.RunInfo{
		Repository: "asymptote-labs/agent-beacon",
		RunID:      "123",
		RunAttempt: "2",
	}
	destinations, err := resolveUploadDestinations([]string{"s3"}, run, "/tmp/runtime.jsonl", time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatalf("resolveUploadDestinations returned error: %v", err)
	}
	if len(destinations) != 1 {
		t.Fatalf("destinations = %d, want 1", len(destinations))
	}
	dest := destinations[0]
	if dest.Key != "logs/asymptote-labs/agent-beacon/123/2/runtime.jsonl" {
		t.Fatalf("Key = %q", dest.Key)
	}
	if dest.URI != "s3://beacon-ci/logs/asymptote-labs/agent-beacon/123/2/runtime.jsonl" {
		t.Fatalf("URI = %q", dest.URI)
	}
}

func TestResolveUploadDestinationsBuildsFallbackObjectKeys(t *testing.T) {
	t.Setenv(EnvGCSBucket, "beacon-ci")
	startedAt := time.Unix(0, 42).UTC()
	destinations, err := resolveUploadDestinations([]string{"gcs"}, nil, "/tmp/runtime.jsonl", startedAt)
	if err != nil {
		t.Fatalf("resolveUploadDestinations returned error: %v", err)
	}
	if got, want := destinations[0].Key, "ci/42/runtime.jsonl"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
	if got, want := destinations[0].URI, "gs://beacon-ci/ci/42/runtime.jsonl"; got != want {
		t.Fatalf("URI = %q, want %q", got, want)
	}
}

func TestUploadArtifactRunsS3Command(t *testing.T) {
	calls, restore := stubUploadCommands(t, map[string]bool{"aws": true}, nil)
	defer restore()

	dest := UploadDestination{Provider: UploadS3, URI: "s3://bucket/key"}
	result, err := uploadArtifact(context.Background(), "/tmp/runtime.jsonl", dest)
	if err != nil {
		t.Fatalf("uploadArtifact returned error: %v", err)
	}
	if result.Status != "ok" || result.Target != dest.URI {
		t.Fatalf("result = %#v", result)
	}
	if got, want := strings.Join(*calls, "\n"), "aws s3 cp /tmp/runtime.jsonl s3://bucket/key"; got != want {
		t.Fatalf("upload command = %q, want %q", got, want)
	}
}

func TestUploadArtifactRunsGCloudCommand(t *testing.T) {
	calls, restore := stubUploadCommands(t, map[string]bool{"gcloud": true}, nil)
	defer restore()

	dest := UploadDestination{Provider: UploadGCS, URI: "gs://bucket/key"}
	if _, err := uploadArtifact(context.Background(), "/tmp/runtime.jsonl", dest); err != nil {
		t.Fatalf("uploadArtifact returned error: %v", err)
	}
	if got, want := strings.Join(*calls, "\n"), "gcloud storage cp /tmp/runtime.jsonl gs://bucket/key"; got != want {
		t.Fatalf("upload command = %q, want %q", got, want)
	}
}

func TestUploadArtifactFallsBackToGsutil(t *testing.T) {
	calls, restore := stubUploadCommands(t, map[string]bool{"gsutil": true}, nil)
	defer restore()

	dest := UploadDestination{Provider: UploadGCS, URI: "gs://bucket/key"}
	if _, err := uploadArtifact(context.Background(), "/tmp/runtime.jsonl", dest); err != nil {
		t.Fatalf("uploadArtifact returned error: %v", err)
	}
	if got, want := strings.Join(*calls, "\n"), "gsutil cp /tmp/runtime.jsonl gs://bucket/key"; got != want {
		t.Fatalf("upload command = %q, want %q", got, want)
	}
}

func TestUploadArtifactMissingCLI(t *testing.T) {
	_, restore := stubUploadCommands(t, nil, nil)
	defer restore()

	result, err := uploadArtifact(context.Background(), "/tmp/runtime.jsonl", UploadDestination{Provider: UploadS3, URI: "s3://bucket/key"})
	if err == nil || !strings.Contains(err.Error(), "aws CLI") {
		t.Fatalf("uploadArtifact error = %v, want missing aws CLI", err)
	}
	if result.Status != "fail" {
		t.Fatalf("result status = %q, want fail", result.Status)
	}
}

func TestUploadArtifactCommandFailure(t *testing.T) {
	_, restore := stubUploadCommands(t, map[string]bool{"aws": true}, errors.New("boom"))
	defer restore()

	result, err := uploadArtifact(context.Background(), "/tmp/runtime.jsonl", UploadDestination{Provider: UploadS3, URI: "s3://bucket/key"})
	if err == nil || !strings.Contains(err.Error(), "upload s3") {
		t.Fatalf("uploadArtifact error = %v, want upload failure", err)
	}
	if result.Status != "fail" || result.Message != "boom" {
		t.Fatalf("result = %#v", result)
	}
}

func stubUploadCommands(t *testing.T, available map[string]bool, commandErr error) (*[]string, func()) {
	t.Helper()
	oldLookPath := uploadLookPath
	oldRun := runUploadCommand
	var calls []string
	uploadLookPath = func(name string) (string, error) {
		if available[name] {
			return filepath.Join("/bin", name), nil
		}
		return "", exec.ErrNotFound
	}
	runUploadCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if commandErr != nil {
			return []byte(commandErr.Error()), commandErr
		}
		return nil, nil
	}
	return &calls, func() {
		if t.Failed() {
			t.Logf("upload command calls: %s", fmt.Sprint(calls))
		}
		uploadLookPath = oldLookPath
		runUploadCommand = oldRun
	}
}
