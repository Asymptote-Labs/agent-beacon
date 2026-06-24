package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const sampleManifest = `{
  "schema": 1,
  "version": "0.0.69",
  "min_supported_version": "0.0.40",
  "team_id": "TEAMID",
  "pkg_identifier": "ai.asymptote.beacon.endpoint",
  "artifacts": {
    "darwin_arm64": {"url": "https://example.test/Beacon-0.0.69-arm64.pkg", "sha256": "aaa"}
  }
}`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Version != "0.0.69" {
		t.Fatalf("version = %q", m.Version)
	}
	if m.TeamID != "TEAMID" || m.PkgIdentifier != "ai.asymptote.beacon.endpoint" {
		t.Fatalf("metadata not parsed: %+v", m)
	}
	a, ok := m.ArtifactFor("darwin_arm64")
	if !ok || a.URL == "" || a.SHA256 != "aaa" {
		t.Fatalf("arm64 artifact = %+v ok=%v", a, ok)
	}
	if _, ok := m.ArtifactFor("linux_arm64"); ok {
		t.Fatalf("unexpected artifact for linux_arm64")
	}
	if _, ok := m.ArtifactFor("darwin_amd64"); ok {
		t.Fatalf("unexpected amd64 artifact in arm64-only manifest")
	}
}

func TestParseManifestRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"empty version":   `{"schema":1,"artifacts":{}}`,
		"missing schema":  `{"version":"0.0.1","artifacts":{}}`,
		"future schema":   `{"schema":99,"version":"0.0.1","artifacts":{}}`,
		"non-release ver": `{"schema":1,"version":"latest","artifacts":{}}`,
		"malformed json":  `{`,
	}
	for name, body := range cases {
		if _, err := ParseManifest([]byte(body)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestArtifactForRequiresURLAndSHA(t *testing.T) {
	m := UpdateManifest{Artifacts: map[string]Artifact{
		"darwin_arm64": {URL: "https://x", SHA256: ""},
		"darwin_amd64": {URL: "", SHA256: "abc"},
	}}
	if _, ok := m.ArtifactFor("darwin_arm64"); ok {
		t.Errorf("artifact without sha256 should be rejected")
	}
	if _, ok := m.ArtifactFor("darwin_amd64"); ok {
		t.Errorf("artifact without url should be rejected")
	}
}

func TestEvaluateManifest(t *testing.T) {
	m, err := ParseManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("update available", func(t *testing.T) {
		r, err := EvaluateManifest("0.0.67", m)
		if err != nil {
			t.Fatal(err)
		}
		if !r.UpdateAvailable {
			t.Fatalf("expected update available")
		}
		if r.BelowMinSupported {
			t.Fatalf("0.0.67 should be above min supported")
		}
		if r.LatestVersion != "v0.0.69" || r.CurrentVersion != "v0.0.67" {
			t.Fatalf("display versions: %+v", r)
		}
	})

	t.Run("already current", func(t *testing.T) {
		r, _ := EvaluateManifest("0.0.69", m)
		if r.UpdateAvailable {
			t.Fatalf("should not report update when current")
		}
	})

	t.Run("newer than manifest", func(t *testing.T) {
		r, _ := EvaluateManifest("0.0.80", m)
		if r.UpdateAvailable {
			t.Fatalf("should not downgrade")
		}
	})

	t.Run("below min supported", func(t *testing.T) {
		r, _ := EvaluateManifest("0.0.30", m)
		if !r.UpdateAvailable || !r.BelowMinSupported {
			t.Fatalf("expected update + below-min: %+v", r)
		}
	})

	t.Run("dev build", func(t *testing.T) {
		r, _ := EvaluateManifest("dev", m)
		if !r.CurrentIsDev || r.UpdateAvailable {
			t.Fatalf("dev build should be skipped: %+v", r)
		}
	})
}

func TestManifestSourceFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleManifest))
	}))
	defer srv.Close()

	src := ManifestSource{Client: &http.Client{Timeout: 2 * time.Second}, Endpoint: srv.URL}
	m, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if m.Version != "0.0.69" {
		t.Fatalf("version = %q", m.Version)
	}
}

func TestManifestSourceFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	src := ManifestSource{Endpoint: srv.URL}
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatalf("expected error on HTTP 404")
	}
}

func TestManifestURLEnvOverride(t *testing.T) {
	t.Setenv(ManifestURLEnv, "https://internal.example/manifest.json")
	if got := ManifestURL(); got != "https://internal.example/manifest.json" {
		t.Fatalf("ManifestURL = %q", got)
	}
	t.Setenv(ManifestURLEnv, "")
	if got := ManifestURL(); got != DefaultManifestURL {
		t.Fatalf("ManifestURL fallback = %q", got)
	}
}
