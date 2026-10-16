package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

func signedBy(team string) runnerFunc {
	return func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "pkgutil" {
			return "Status: signed by a developer certificate issued by Apple for distribution\nCertificate Chain:\n 1. Developer ID Installer: Example (" + team + ")\n 2. Developer ID Certification Authority\n 3. Apple Root CA", nil
		}
		return "", nil
	}
}

// The installer signer must be Beacon's team exactly. Notarization alone only says some Developer
// ID signed the package, and the team the unsigned manifest names cannot widen that.
func TestVerifyGatekeeperRequiresBeaconsTeamExactly(t *testing.T) {
	a := NewApplier("0.0.1")
	pkg := filepath.Join(t.TempDir(), "Beacon.pkg")
	for _, signer := range []string{"OTHERTEAM1", AppleTeamID + "X", "X" + AppleTeamID, ""} {
		a.run = signedBy(signer)
		if err := a.verifyGatekeeper(context.Background(), pkg); err == nil {
			t.Fatalf("package signed by %q accepted", signer)
		}
	}
	a.run = signedBy(AppleTeamID)
	if err := a.verifyGatekeeper(context.Background(), pkg); err != nil {
		t.Fatalf("package signed by Beacon's team refused: %v", err)
	}
}

func TestDownloadRefusesInsecureURLs(t *testing.T) {
	a := NewApplier("0.0.1")
	dest := filepath.Join(t.TempDir(), "pkg")
	if err := a.download(context.Background(), "http://updates.example.com/beacon.pkg", dest); err == nil {
		t.Fatal("download accepted a plain http URL")
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://updates.example.com/beacon.pkg", http.StatusFound)
	}))
	defer plain.Close()
	if err := a.download(context.Background(), plain.URL, dest); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("download followed a redirect onto plain http: %v", err)
	}
}

func TestRequireSecureURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://github.com/x":   true,
		"http://127.0.0.1:8080/": true,
		"http://localhost/x":     true,
		"http://[::1]/x":         true,
		"http://example.com/x":   false,
		"ftp://example.com/x":    false,
		"https:///x":             false,
		"file:///etc/passwd":     false,
	} {
		if err := updatecheck.RequireSecureURL(raw); (err == nil) != ok {
			t.Errorf("RequireSecureURL(%q) = %v, want ok=%v", raw, err, ok)
		}
	}
}
