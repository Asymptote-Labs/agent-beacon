package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ConfigDirName       = ".beacon"
	AuthDirName         = "auth"
	CredentialsFileName = "credentials.json"
)

type Credentials struct {
	Token       string    `json:"token"`
	TokenPrefix string    `json:"token_prefix"`
	ExpiresAt   time.Time `json:"expires_at"`
	UserID      string    `json:"user_id"`
	Email       string    `json:"email,omitempty"`
	OrgID       string    `json:"org_id,omitempty"`
	OrgName     string    `json:"org_name,omitempty"`
}

func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDirName, AuthDirName), nil
}

func EnsureConfigDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func CredentialsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, CredentialsFileName), nil
}

func SaveCredentials(creds *Credentials) error {
	if creds == nil {
		return fmt.Errorf("credentials are required")
	}
	if err := creds.validateRequiredFields(); err != nil {
		return err
	}
	if _, err := EnsureConfigDir(); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}
	path, err := CredentialsPath()
	if err != nil {
		return fmt.Errorf("failed to resolve credentials path: %w", err)
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials: %w", err)
	}
	return os.Chmod(path, 0600)
}

func LoadCredentials() (*Credentials, error) {
	path, err := CredentialsPath()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve credentials path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}
	return &creds, nil
}

func DeleteCredentials() error {
	path, err := CredentialsPath()
	if err != nil {
		return fmt.Errorf("failed to resolve credentials path: %w", err)
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to delete credentials: %w", err)
	}
	return nil
}

func IsLoggedIn() bool {
	creds, err := LoadCredentials()
	if err != nil || creds == nil {
		return false
	}
	return creds.validateRequiredFields() == nil && !creds.IsExpired()
}

func (c *Credentials) validateRequiredFields() error {
	if c == nil {
		return fmt.Errorf("credentials are required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("credentials token is required")
	}
	if strings.TrimSpace(c.UserID) == "" {
		return fmt.Errorf("credentials user ID is required")
	}
	return nil
}

func (c *Credentials) IsExpired() bool {
	if c == nil || c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

func (c *Credentials) ExpiresIn() string {
	if c == nil || c.ExpiresAt.IsZero() {
		return "never"
	}
	if c.IsExpired() {
		return "expired"
	}
	duration := time.Until(c.ExpiresAt)
	days := int(duration.Hours() / 24)
	if days > 1 {
		return fmt.Sprintf("%d days", days)
	}
	if days == 1 {
		return "1 day"
	}
	hours := int(duration.Hours())
	if hours > 1 {
		return fmt.Sprintf("%d hours", hours)
	}
	if hours == 1 {
		return "1 hour"
	}
	minutes := int(duration.Minutes())
	if minutes > 1 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	return "less than a minute"
}
