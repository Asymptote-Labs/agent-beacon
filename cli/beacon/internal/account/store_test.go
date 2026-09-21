package account

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testSession() Session {
	return Session{
		BaseURL:     "https://beacon.sh",
		AccessToken: "bcn_cli_super_secret",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		User:        User{ID: "usr_1", Email: "person@example.com", Name: "Beacon User"},
		Organizations: []Organization{{
			ID: "org_1", Slug: "acme", Name: "Acme",
		}},
		Scopes: []string{"profile:read"},
	}
}

func TestSaveLoadInspectAndRemoveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	session := testSession()
	if err := Save(session); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.AccessToken != session.AccessToken || loaded.User.Email != session.User.Email {
		t.Fatalf("loaded session = %#v", loaded)
	}
	if loaded.SchemaVersion != SchemaVersion || loaded.CreatedAt.IsZero() {
		t.Fatalf("session metadata = %#v", loaded)
	}
	session.AccessToken = "bcn_cli_rotated_secret"
	if err := Save(session); err != nil {
		t.Fatalf("replace session: %v", err)
	}
	loaded, err = Load()
	if err != nil || loaded.AccessToken != session.AccessToken {
		t.Fatalf("replaced session = %#v, %v", loaded, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("session mode = %o, want 600", info.Mode().Perm())
		}
	}

	status := Inspect(time.Now())
	if !status.SignedIn || status.User.Email != session.User.Email || status.SessionPath != Path() {
		t.Fatalf("status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), session.AccessToken) || strings.Contains(string(encoded), "access_token") {
		t.Fatalf("status leaked access token: %s", encoded)
	}

	if err := Remove(); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if _, err := Load(); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Load after remove = %v", err)
	}
}

func TestInspectMarksExpiredSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	session := testSession()
	session.ExpiresAt = time.Now().Add(-time.Minute)
	if err := Save(session); err != nil {
		t.Fatal(err)
	}
	status := Inspect(time.Now())
	if !status.SignedIn || !status.Expired {
		t.Fatalf("status = %#v", status)
	}
}

func TestSaveRejectsIncompleteSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := Save(Session{BaseURL: "https://beacon.sh"}); err == nil {
		t.Fatal("expected missing token rejection")
	}
	session := testSession()
	session.BaseURL = "http://beacon.example.com"
	if err := Save(session); err == nil {
		t.Fatal("expected insecure base URL rejection")
	}
}
