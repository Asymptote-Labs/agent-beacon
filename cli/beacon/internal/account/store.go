// Package account implements Beacon CLI account authentication and credential storage.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	SessionPath   = ".beacon/auth/session.json"
	SchemaVersion = 1
)

var ErrNotSignedIn = errors.New("not signed in")

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type Organization struct {
	ID   string `json:"id"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name"`
}

// Session is the private credential returned by the beacon.sh CLI exchange.
// AccessToken is deliberately omitted from all status/result types.
type Session struct {
	SchemaVersion      int            `json:"schema_version"`
	BaseURL            string         `json:"base_url"`
	AccessToken        string         `json:"access_token"`
	TokenType          string         `json:"token_type"`
	ExpiresAt          time.Time      `json:"expires_at"`
	CreatedAt          time.Time      `json:"created_at"`
	User               User           `json:"user"`
	Organizations      []Organization `json:"organizations,omitempty"`
	ActiveOrganization *Organization  `json:"active_organization,omitempty"`
	Scopes             []string       `json:"scopes,omitempty"`
}

type Status struct {
	SignedIn           bool           `json:"signed_in"`
	Expired            bool           `json:"expired,omitempty"`
	BaseURL            string         `json:"base_url,omitempty"`
	ExpiresAt          time.Time      `json:"expires_at,omitempty"`
	User               User           `json:"user,omitempty"`
	Organizations      []Organization `json:"organizations,omitempty"`
	ActiveOrganization *Organization  `json:"active_organization,omitempty"`
	Scopes             []string       `json:"scopes,omitempty"`
	SessionPath        string         `json:"session_path"`
}

func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", SessionPath)
	}
	return filepath.Join(home, SessionPath)
}

func Load() (*Session, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotSignedIn
	}
	if err != nil {
		return nil, fmt.Errorf("read Beacon session: %w", err)
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse Beacon session: %w", err)
	}
	if err := validateSession(&session); err != nil {
		return nil, fmt.Errorf("invalid Beacon session: %w", err)
	}
	return &session, nil
}

func Save(session Session) error {
	if session.SchemaVersion == 0 {
		session.SchemaVersion = SchemaVersion
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	if err := validateSession(&session); err != nil {
		return err
	}
	path := Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows does not replace an existing destination with os.Rename.
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(path, 0o600)
	}
	return nil
}

func Remove() error {
	err := os.Remove(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func Inspect(now time.Time) Status {
	status := Status{SessionPath: Path()}
	session, err := Load()
	if err != nil {
		return status
	}
	status.SignedIn = true
	status.Expired = session.Expired(now)
	status.BaseURL = session.BaseURL
	status.ExpiresAt = session.ExpiresAt
	status.User = session.User
	status.Organizations = append([]Organization(nil), session.Organizations...)
	status.ActiveOrganization = session.ActiveOrganization
	status.Scopes = append([]string(nil), session.Scopes...)
	return status
}

func (s Session) Expired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt)
}

func validateSession(session *Session) error {
	switch {
	case session.SchemaVersion != 0 && session.SchemaVersion != SchemaVersion:
		return fmt.Errorf("unsupported schema version %d", session.SchemaVersion)
	case session.BaseURL == "":
		return errors.New("base URL is required")
	case !secureURL(session.BaseURL):
		return errors.New("base URL must use HTTPS or loopback HTTP")
	case session.AccessToken == "":
		return errors.New("access token is required")
	case session.User.ID == "" || session.User.Email == "":
		return errors.New("user id and email are required")
	}
	if session.TokenType == "" {
		session.TokenType = "Bearer"
	}
	if !strings.EqualFold(session.TokenType, "Bearer") {
		return fmt.Errorf("unsupported token type %q", session.TokenType)
	}
	session.TokenType = "Bearer"
	return nil
}
