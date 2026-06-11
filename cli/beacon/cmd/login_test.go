package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
)

type stubLoginService struct {
	creds        *auth.Credentials
	loginCreds   *auth.Credentials
	loginErr     error
	saveErr      error
	loggedIn     bool
	loginCalled  bool
	saveCalled   bool
	capturedOpts auth.LoginOptions
}

func (s *stubLoginService) Login(opts auth.LoginOptions) (*auth.Credentials, error) {
	s.loginCalled = true
	s.capturedOpts = opts
	if s.loginErr != nil {
		return nil, s.loginErr
	}
	return s.loginCreds, nil
}

func (s *stubLoginService) LoadCredentials() (*auth.Credentials, error) {
	return s.creds, nil
}

func (s *stubLoginService) SaveCredentials(creds *auth.Credentials) error {
	s.saveCalled = true
	s.creds = creds
	return s.saveErr
}

func (s *stubLoginService) IsLoggedIn() bool {
	return s.loggedIn
}

func TestLoginCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"login"})
	if err != nil {
		t.Fatalf("Find login returned error: %v", err)
	}
	if cmd == nil {
		t.Fatal("login command not registered")
	}
	if cmd.Flags().Lookup("dashboard-url") == nil {
		t.Fatal("login command missing --dashboard-url")
	}
	if cmd.Flags().Lookup("force") == nil {
		t.Fatal("login command missing --force")
	}
}

func TestRunLoginAlreadyLoggedIn(t *testing.T) {
	service := &stubLoginService{
		loggedIn: true,
		creds: &auth.Credentials{
			Email:   "user@example.test",
			OrgName: "Example Org",
		},
	}
	restore := setLoginServiceForTest(t, service)
	defer restore()
	restoreOpts := setLoginOptsForTest(t, loginOptions{})
	defer restoreOpts()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("runLogin returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Already logged in as user@example.test",
		"Organization: Example Org",
		"beacon login --force",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
	if service.loginCalled {
		t.Fatal("Login called despite existing credentials")
	}
}

func TestRunLoginForceReplacesCredentials(t *testing.T) {
	service := &stubLoginService{
		loggedIn: true,
		creds:    &auth.Credentials{Email: "old@example.test"},
		loginCreds: &auth.Credentials{
			Token:       "secret-token",
			TokenPrefix: "asym_123",
			UserID:      "user-1",
			Email:       "new@example.test",
			OrgName:     "Example Org",
		},
	}
	restore := setLoginServiceForTest(t, service)
	defer restore()
	restoreOpts := setLoginOptsForTest(t, loginOptions{force: true, dashboardURL: "https://dashboard.example"})
	defer restoreOpts()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runLogin(cmd, nil); err != nil {
		t.Fatalf("runLogin returned error: %v", err)
	}
	if !service.loginCalled {
		t.Fatal("Login was not called")
	}
	if !service.saveCalled {
		t.Fatal("SaveCredentials was not called")
	}
	if service.capturedOpts.DashboardURL != "https://dashboard.example" {
		t.Fatalf("DashboardURL = %q, want override", service.capturedOpts.DashboardURL)
	}
	got := out.String()
	for _, want := range []string{
		"Success! Logged in as new@example.test",
		"Organization: Example Org",
		"Beacon endpoint telemetry remains local-only",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestRunLoginReturnsLoginError(t *testing.T) {
	service := &stubLoginService{loginErr: errors.New("browser closed")}
	restore := setLoginServiceForTest(t, service)
	defer restore()
	restoreOpts := setLoginOptsForTest(t, loginOptions{})
	defer restoreOpts()

	cmd := &cobra.Command{}
	err := runLogin(cmd, nil)
	if err == nil {
		t.Fatal("runLogin error = nil, want error")
	}
	if !strings.Contains(err.Error(), "login failed: browser closed") {
		t.Fatalf("error = %v, want login failure", err)
	}
}

func setLoginServiceForTest(t *testing.T, service loginService) func() {
	t.Helper()
	old := newLoginService
	newLoginService = func() loginService {
		return service
	}
	return func() { newLoginService = old }
}

func setLoginOptsForTest(t *testing.T, opts loginOptions) func() {
	t.Helper()
	old := loginOpts
	loginOpts = opts
	return func() { loginOpts = old }
}
