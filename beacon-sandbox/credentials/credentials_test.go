package credentials

import (
	"strings"
	"testing"
)

// The precedence order is a security decision, not a convenience one: a provider-stored secret
// never enters this process, so it must win over an ambient environment variable that might be
// stale or belong to a different account.

func TestProviderSecretWinsOverEverything(t *testing.T) {
	t.Setenv(EnvVar, "sk-ant-from-env")
	got, err := Resolve(Options{ProviderSecretName: "stored", KeyCommand: "echo sk-ant-from-cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceProviderSecret {
		t.Fatalf("expected the provider secret to win, got %s", got.Source)
	}
	if got.Value != "" {
		t.Error("a provider secret must not pull the value into this process")
	}
	if got.ProviderSecretName != "stored" {
		t.Errorf("secret name = %q", got.ProviderSecretName)
	}
}

func TestCommandWinsOverEnv(t *testing.T) {
	t.Setenv(EnvVar, "sk-ant-from-env")
	got, err := Resolve(Options{KeyCommand: "echo sk-ant-from-cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceCommand || got.Value != "sk-ant-from-cmd" {
		t.Fatalf("expected the command to win, got %s / %q", got.Source, got.Value)
	}
}

func TestEnvIsTheFallback(t *testing.T) {
	t.Setenv(EnvVar, "  sk-ant-padded  ")
	got, err := Resolve(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceEnv {
		t.Fatalf("expected env, got %s", got.Source)
	}
	if got.Value != "sk-ant-padded" {
		t.Errorf("value should be trimmed, got %q", got.Value)
	}
}

// An unset credential must produce an error that lists every way to supply one, since this is
// the most common first-run failure.
func TestNoCredentialErrorListsAllThreeOptions(t *testing.T) {
	t.Setenv(EnvVar, "")
	got, err := Resolve(Options{})
	if err == nil {
		t.Fatal("expected an error when no credential is available")
	}
	if got.Source != SourceNone {
		t.Errorf("source = %s, want none", got.Source)
	}
	for _, want := range []string{"--modal-secret", "--api-key-command", EnvVar} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s, got: %v", want, err)
		}
	}
}

// The leak check searches collected artifacts for the credential. It can only do that when the
// value is local, and the caller must be able to tell the difference so an unrunnable check is
// never reported as a passing one.
func TestLeakCheckPossibleTracksValueAvailability(t *testing.T) {
	if (Resolved{Source: SourceProviderSecret, ProviderSecretName: "s"}).LeakCheckPossible() {
		t.Error("a provider secret has no local value, so the leak check cannot run")
	}
	if !(Resolved{Source: SourceEnv, Value: "sk-ant-x"}).LeakCheckPossible() {
		t.Error("a local value means the leak check can run")
	}
	if (Resolved{Source: SourceNone}).LeakCheckPossible() {
		t.Error("no credential means no leak check")
	}
}

// Describe is printed to the terminal and stored in run metadata, so it must never contain the
// key itself.
func TestDescribeNeverLeaksTheValue(t *testing.T) {
	secret := "sk-ant-do-not-print-me"
	for _, r := range []Resolved{
		{Source: SourceEnv, Value: secret},
		{Source: SourceCommand, Value: secret},
		{Source: SourceProviderSecret, ProviderSecretName: "stored"},
		{Source: SourceNone},
	} {
		if strings.Contains(r.Describe(), secret) {
			t.Errorf("Describe leaked the credential for source %s: %q", r.Source, r.Describe())
		}
		if r.Describe() == "" {
			t.Errorf("source %s produced an empty description", r.Source)
		}
	}
	// The provider-secret description must state the consequence, not just the source.
	d := (Resolved{Source: SourceProviderSecret, ProviderSecretName: "stored"}).Describe()
	if !strings.Contains(d, "leak check") {
		t.Errorf("provider-secret description should note the leak check cannot run, got %q", d)
	}
}

func TestKeyCommandFailuresAreDiagnosable(t *testing.T) {
	cases := map[string]string{
		"nonzero exit":    "exit 3",
		"no output":       "true",
		"multiline":       "printf 'line1\\nline2\\n'",
		"only whitespace": "printf '   '",
	}
	for name, cmd := range cases {
		if _, err := Resolve(Options{KeyCommand: cmd}); err == nil {
			t.Errorf("%s: expected an error from %q", name, cmd)
		}
	}
}

// A helper that prints the key with a trailing newline is the normal case, not an error.
func TestKeyCommandTrimsTrailingNewline(t *testing.T) {
	got, err := Resolve(Options{KeyCommand: "printf 'sk-ant-clean\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "sk-ant-clean" {
		t.Errorf("value = %q, want the trimmed key", got.Value)
	}
}

// A blank flag value must not be mistaken for an intent to use that path.
func TestWhitespaceOnlyOptionsFallThrough(t *testing.T) {
	t.Setenv(EnvVar, "sk-ant-env")
	got, err := Resolve(Options{ProviderSecretName: "   ", KeyCommand: "\t"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceEnv {
		t.Errorf("blank options should fall through to env, got %s", got.Source)
	}
}
