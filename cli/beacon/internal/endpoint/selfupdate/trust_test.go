package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

func gatekeeperRunner(pkgSigner, installedTeam string) runnerFunc {
	return func(ctx context.Context, name string, args ...string) (string, error) {
		switch name {
		case "pkgutil":
			return "Status: signed by a developer certificate issued by Apple for distribution\nCertificate Chain:\n 1. Developer ID Installer: Example (" + pkgSigner + ")\n 2. Developer ID Certification Authority\n 3. Apple Root CA", nil
		case "codesign":
			if installedTeam == "" {
				return "code object is not signed at all", fmt.Errorf("exit status 1")
			}
			return "Executable=/opt/beacon/bin/beacon\nAuthority=Developer ID Application: Example (" + installedTeam + ")\nTeamIdentifier=" + installedTeam + "\n", nil
		default:
			return "", nil
		}
	}
}

// The manifest is unsigned, so the team it names must not decide whose package is installed when
// the installed binary says which team Beacon is.
func TestExpectedTeamIDPrefersInstalledSigner(t *testing.T) {
	a := NewApplier("0.0.1")
	a.InstallPrefix = t.TempDir()
	a.run = gatekeeperRunner("EVILTEAM01", "BEACONTM01")
	if got := a.expectedTeamID(context.Background(), "EVILTEAM01"); got != "BEACONTM01" {
		t.Fatalf("expectedTeamID = %q, want the installed binary's team", got)
	}
	err := a.verifyGatekeeper(context.Background(), filepath.Join(t.TempDir(), "Beacon.pkg"), a.expectedTeamID(context.Background(), "EVILTEAM01"))
	if err == nil || !strings.Contains(err.Error(), "EVILTEAM01") {
		t.Fatalf("verifyGatekeeper = %v, want a signer mismatch", err)
	}
}

func TestVerifyGatekeeperRequiresExactTeam(t *testing.T) {
	a := NewApplier("0.0.1")
	pkg := filepath.Join(t.TempDir(), "Beacon.pkg")
	for _, tc := range []struct{ signer, want string }{
		{"BEACONTM01X", "BEACONTM01"}, // substring of the signer is not the signer
		{"XBEACONTM01", "BEACONTM01"},
		{"OTHERTEAM1", ""}, // no expected team is a refusal, not a pass
	} {
		a.run = gatekeeperRunner(tc.signer, "")
		if err := a.verifyGatekeeper(context.Background(), pkg, tc.want); err == nil {
			t.Fatalf("signer %q accepted for expected team %q", tc.signer, tc.want)
		}
	}
	a.run = gatekeeperRunner("BEACONTM01", "")
	if err := a.verifyGatekeeper(context.Background(), pkg, "BEACONTM01"); err != nil {
		t.Fatalf("matching signer refused: %v", err)
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
