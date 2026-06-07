package ci

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	UploadS3  = "s3"
	UploadGCS = "gcs"
)

const (
	EnvS3Bucket  = "BEACON_CI_S3_BUCKET"
	EnvS3Prefix  = "BEACON_CI_S3_PREFIX"
	EnvGCSBucket = "BEACON_CI_GCS_BUCKET"
	EnvGCSPrefix = "BEACON_CI_GCS_PREFIX"
)

var uploadCredentialEnv = []string{
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_PROFILE",
	"AWS_WEB_IDENTITY_TOKEN_FILE",
	"GOOGLE_APPLICATION_CREDENTIALS",
	"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE",
}

var (
	uploadLookPath   = exec.LookPath
	runUploadCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
)

type UploadDestination struct {
	Provider string `json:"provider"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	URI      string `json:"uri"`
}

type UploadResult struct {
	Provider string `json:"provider"`
	Target   string `json:"target"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}

func resolveUploadDestinations(providers []string, run *schema.RunInfo, logPath string, startedAt time.Time) ([]UploadDestination, error) {
	normalized, err := normalizeUploads(providers)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	destinations := make([]UploadDestination, 0, len(normalized))
	for _, provider := range normalized {
		switch provider {
		case UploadS3:
			dest, err := uploadDestination(provider, os.Getenv(EnvS3Bucket), os.Getenv(EnvS3Prefix), run, logPath, startedAt)
			if err != nil {
				return nil, err
			}
			destinations = append(destinations, dest)
		case UploadGCS:
			dest, err := uploadDestination(provider, os.Getenv(EnvGCSBucket), os.Getenv(EnvGCSPrefix), run, logPath, startedAt)
			if err != nil {
				return nil, err
			}
			destinations = append(destinations, dest)
		}
	}
	return destinations, nil
}

func normalizeUploads(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	var providers []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			provider := strings.ToLower(strings.TrimSpace(part))
			if provider == "" {
				continue
			}
			switch provider {
			case UploadS3, UploadGCS:
				if _, ok := seen[provider]; ok {
					continue
				}
				seen[provider] = struct{}{}
				providers = append(providers, provider)
			default:
				return nil, fmt.Errorf("unsupported --upload %q; supported values are %s and %s", part, UploadS3, UploadGCS)
			}
		}
	}
	return providers, nil
}

func uploadDestination(provider, bucket, prefix string, run *schema.RunInfo, logPath string, startedAt time.Time) (UploadDestination, error) {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return UploadDestination{}, fmt.Errorf("--upload %s requires a bucket; set %s", provider, uploadBucketEnv(provider))
	}
	key := uploadObjectKey(prefix, run, logPath, startedAt)
	uri := fmt.Sprintf("%s://%s/%s", uploadScheme(provider), bucket, key)
	return UploadDestination{Provider: provider, Bucket: bucket, Key: key, URI: uri}, nil
}

func uploadObjectKey(prefix string, run *schema.RunInfo, logPath string, startedAt time.Time) string {
	name := filepath.Base(logPath)
	if strings.TrimSpace(name) == "" || name == "." || name == string(filepath.Separator) {
		name = "runtime.jsonl"
	}
	parts := cleanKeyParts(prefix)
	if run != nil && strings.TrimSpace(run.Repository) != "" && strings.TrimSpace(run.RunID) != "" {
		parts = append(parts, cleanKeyParts(run.Repository)...)
		parts = append(parts, cleanKeyPart(run.RunID))
		if strings.TrimSpace(run.RunAttempt) != "" {
			parts = append(parts, cleanKeyPart(run.RunAttempt))
		}
	} else {
		if startedAt.IsZero() {
			startedAt = time.Now().UTC()
		}
		parts = append(parts, "ci", fmt.Sprintf("%d", startedAt.UTC().UnixNano()))
	}
	parts = append(parts, cleanKeyPart(name))
	return path.Join(parts...)
}

func cleanKeyParts(value string) []string {
	var parts []string
	for _, part := range strings.Split(strings.Trim(value, "/"), "/") {
		if cleaned := cleanKeyPart(part); cleaned != "" {
			parts = append(parts, cleaned)
		}
	}
	return parts
}

func cleanKeyPart(value string) string {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.ReplaceAll(cleaned, "\\", "-")
	cleaned = strings.Trim(cleaned, "/")
	if cleaned == "." || cleaned == ".." {
		return ""
	}
	return cleaned
}

func uploadBucketEnv(provider string) string {
	if provider == UploadGCS {
		return EnvGCSBucket
	}
	return EnvS3Bucket
}

func uploadScheme(provider string) string {
	if provider == UploadGCS {
		return "gs"
	}
	return "s3"
}

func (s *Session) UploadArtifacts(ctx context.Context) ([]UploadResult, error) {
	if s == nil || len(s.Uploads) == 0 {
		return nil, nil
	}
	results := make([]UploadResult, 0, len(s.Uploads))
	for _, dest := range s.Uploads {
		result, err := uploadArtifact(ctx, s.LogPath, dest)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func uploadArtifact(ctx context.Context, logPath string, dest UploadDestination) (UploadResult, error) {
	var name string
	var args []string
	switch dest.Provider {
	case UploadS3:
		if _, err := uploadLookPath("aws"); err != nil {
			return uploadFailure(dest, "aws CLI not found in PATH"), fmt.Errorf("--upload s3 requires the aws CLI in PATH")
		}
		name = "aws"
		args = []string{"s3", "cp", logPath, dest.URI}
	case UploadGCS:
		switch {
		case commandAvailable("gcloud"):
			name = "gcloud"
			args = []string{"storage", "cp", logPath, dest.URI}
		case commandAvailable("gsutil"):
			name = "gsutil"
			args = []string{"cp", logPath, dest.URI}
		default:
			return uploadFailure(dest, "gcloud or gsutil not found in PATH"), fmt.Errorf("--upload gcs requires gcloud or gsutil in PATH")
		}
	default:
		return uploadFailure(dest, "unsupported provider"), fmt.Errorf("unsupported upload provider %q", dest.Provider)
	}
	output, err := runUploadCommand(ctx, name, args...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return uploadFailure(dest, message), fmt.Errorf("upload %s to %s failed: %w", dest.Provider, dest.URI, err)
	}
	return UploadResult{Provider: dest.Provider, Target: dest.URI, Status: "ok"}, nil
}

func uploadFailure(dest UploadDestination, message string) UploadResult {
	return UploadResult{Provider: dest.Provider, Target: dest.URI, Status: "fail", Message: message}
}

func commandAvailable(name string) bool {
	_, err := uploadLookPath(name)
	return err == nil
}
