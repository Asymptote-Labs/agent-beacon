package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
)

func withAccountFakes(t *testing.T) {
	t.Helper()
	originalLogin := accountLogin
	originalSave := accountSave
	originalLoad := accountLoad
	originalRemove := accountRemove
	originalInspect := accountInspect
	originalRevoke := accountRevoke
	originalNow := accountNow
	originalOpts := accountOpts
	t.Cleanup(func() {
		accountLogin = originalLogin
		accountSave = originalSave
		accountLoad = originalLoad
		accountRemove = originalRemove
		accountInspect = originalInspect
		accountRevoke = originalRevoke
		accountNow = originalNow
		accountOpts = originalOpts
	})
	accountOpts = struct {
		baseURL   string
		noBrowser bool
		json      bool
	}{}
}

func accountFixture() (*account.Session, account.Status) {
	expires := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	organization := account.Organization{ID: "org_1", Name: "Acme"}
	session := &account.Session{
		BaseURL:            "https://beacon.sh",
		AccessToken:        "never-print-this-token",
		TokenType:          "Bearer",
		ExpiresAt:          expires,
		User:               account.User{ID: "usr_1", Email: "person@example.com", Name: "Beacon User"},
		ActiveOrganization: &organization,
	}
	status := account.Status{
		SignedIn:           true,
		BaseURL:            session.BaseURL,
		ExpiresAt:          expires,
		User:               session.User,
		ActiveOrganization: &organization,
		SessionPath:        "/tmp/session.json",
	}
	return session, status
}

func TestRunLoginStoresSessionWithoutPrintingToken(t *testing.T) {
	withAccountFakes(t)
	session, status := accountFixture()
	var saved account.Session
	accountLogin = func(context.Context, account.LoginOptions) (*account.Session, error) { return session, nil }
	accountSave = func(value account.Session) error { saved = value; return nil }
	accountInspect = func(time.Time) account.Status { return status }
	accountNow = func() time.Time { return time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC) }
	var out bytes.Buffer
	loginCmd.SetOut(&out)
	loginCmd.SetErr(&out)

	if err := runLogin(loginCmd, nil); err != nil {
		t.Fatalf("runLogin returned error: %v", err)
	}
	if saved.AccessToken != session.AccessToken {
		t.Fatalf("saved session = %#v", saved)
	}
	if strings.Contains(out.String(), session.AccessToken) {
		t.Fatalf("output leaked access token: %s", out.String())
	}
	for _, want := range []string{"Beacon User <person@example.com>", "Organization: Acme", "0600"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
}

func TestRunWhoamiJSONExcludesCredential(t *testing.T) {
	withAccountFakes(t)
	session, status := accountFixture()
	accountInspect = func(time.Time) account.Status { return status }
	accountOpts.json = true
	var out bytes.Buffer
	whoamiCmd.SetOut(&out)

	if err := runWhoami(whoamiCmd, nil); err != nil {
		t.Fatalf("runWhoami returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("JSON output: %v (%s)", err, out.String())
	}
	if payload["signed_in"] != true || strings.Contains(out.String(), session.AccessToken) || strings.Contains(out.String(), "access_token") {
		t.Fatalf("unsafe whoami output: %s", out.String())
	}
}

func TestRunLogoutRemovesCredentialWhenRevocationFails(t *testing.T) {
	withAccountFakes(t)
	session, _ := accountFixture()
	removed := false
	accountLoad = func() (*account.Session, error) { return session, nil }
	accountRevoke = func(context.Context, account.Session, *http.Client) error { return errors.New("offline") }
	accountRemove = func() error { removed = true; return nil }
	var out, stderr bytes.Buffer
	logoutCmd.SetOut(&out)
	logoutCmd.SetErr(&stderr)

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout returned error: %v", err)
	}
	if !removed {
		t.Fatal("local credential was not removed")
	}
	if !strings.Contains(stderr.String(), "signed out locally") || strings.Contains(out.String()+stderr.String(), session.AccessToken) {
		t.Fatalf("logout output stdout=%q stderr=%q", out.String(), stderr.String())
	}
}

func TestAccountCommandsAreRegistered(t *testing.T) {
	for _, name := range []string{"login", "logout", "whoami"} {
		command, _, err := rootCmd.Find([]string{name})
		if err != nil || command.Name() != name {
			t.Fatalf("root command %q = %v, %v", name, command, err)
		}
	}
}
