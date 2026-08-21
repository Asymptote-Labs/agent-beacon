// Package credentials resolves the Anthropic API key the sandboxed agent needs.
//
// Three ways to supply it, in precedence order, because different setups want different
// trade-offs between convenience and exposure:
//
//	--modal-secret NAME     stored with the provider; the value never enters this process
//	--api-key-command CMD   fetched by running a command (1Password, vault, keychain)
//	ANTHROPIC_API_KEY       plain environment variable
//
// The first option is the most secure and the least verifiable: because the value never
// reaches us, the collected-artifact leak check has nothing to search for and reports
// "unverified" instead of "clean". That is the right failure mode -- an unproven check must
// never read as a passed one -- but it is a real trade-off worth choosing deliberately.
package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// EnvVar is the environment variable the agent expects inside the sandbox.
const EnvVar = "ANTHROPIC_API_KEY"

// Source identifies where a key came from, for reporting.
type Source string

const (
	// SourceProviderSecret means a named secret stored with the sandbox provider.
	SourceProviderSecret Source = "provider-secret"
	// SourceCommand means the output of a user-supplied command.
	SourceCommand Source = "command"
	// SourceEnv means the ambient environment variable.
	SourceEnv Source = "env"
	// SourceNone means no credential could be resolved.
	SourceNone Source = "none"
	// SourceMismatch means a credential is available but is provably not the one the run used,
	// so searching artifacts for it would prove nothing about the original.
	SourceMismatch Source = "mismatch"
)

// Options selects how to resolve the key.
type Options struct {
	// ProviderSecretName, when set, defers entirely to the provider's secret store.
	ProviderSecretName string
	// KeyCommand, when set, is run and its stdout used as the key.
	KeyCommand string
}

// Resolved describes how the agent will be authenticated.
type Resolved struct {
	Source Source
	// Value is the key, empty when Source is SourceProviderSecret or SourceNone.
	Value string
	// ProviderSecretName is set for SourceProviderSecret.
	ProviderSecretName string
}

// LeakCheckPossible reports whether collected artifacts can be searched for this credential.
//
// False for a provider secret, and the caller must surface that as unverified rather than
// silently skipping the check.
func (r Resolved) LeakCheckPossible() bool { return r.Value != "" }

// Fingerprint returns a short, non-reversible identifier for a credential value.
//
// Recorded in run metadata so an offline re-judge can tell whether the key it has is the key the
// run actually used. Searching artifacts for a *different* key finds nothing and reports clean,
// which would silently miss a real disclosure of the original -- so a mismatch has to be
// detectable. The value itself is never persisted; a truncated SHA-256 of a high-entropy API key
// is not invertible, and 48 bits is far more than enough to notice a substitution.
func Fingerprint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// WithheldReason explains, for a verdict finding, why the leak check cannot run. Only
// meaningful when LeakCheckPossible is false.
//
// The wording matters because a bare "value was empty" reads as a defect. For a provider
// secret the absence is the security property being paid for, so the finding should say so
// rather than leave a reader guessing whether something broke.
func (r Resolved) WithheldReason() string {
	switch r.Source {
	case SourceProviderSecret:
		return fmt.Sprintf("it is stored as provider secret %q and never enters this process",
			r.ProviderSecretName)
	case SourceNone:
		return "no credential was resolved"
	case SourceMismatch:
		return "the available credential is not the one this run used, so searching for it " +
			"would say nothing about the original"
	default:
		return "the resolved value was empty"
	}
}

// Describe renders the resolution for human and agent output, never including the value.
func (r Resolved) Describe() string {
	switch r.Source {
	case SourceProviderSecret:
		return fmt.Sprintf("provider secret %q (value not visible locally, so the artifact leak check cannot run)", r.ProviderSecretName)
	case SourceCommand:
		return "command output (value never in the environment or shell history)"
	case SourceEnv:
		return EnvVar + " environment variable"
	case SourceMismatch:
		return "a different " + EnvVar + " than the run used (leak check cannot run)"
	default:
		return "not configured"
	}
}

// Resolve applies the precedence order. It does not validate the key against the API; that is
// the agent session's job, and failing there produces a clearer error than guessing here.
func Resolve(opts Options) (Resolved, error) {
	if name := strings.TrimSpace(opts.ProviderSecretName); name != "" {
		return Resolved{Source: SourceProviderSecret, ProviderSecretName: name}, nil
	}

	if cmdline := strings.TrimSpace(opts.KeyCommand); cmdline != "" {
		value, err := runKeyCommand(cmdline)
		if err != nil {
			return Resolved{Source: SourceNone}, err
		}
		return Resolved{Source: SourceCommand, Value: value}, nil
	}

	if value := strings.TrimSpace(os.Getenv(EnvVar)); value != "" {
		return Resolved{Source: SourceEnv, Value: value}, nil
	}

	return Resolved{Source: SourceNone}, fmt.Errorf(
		"no Anthropic credential found; supply one of:\n"+
			"  --modal-secret NAME      (modal secret create NAME %s=sk-ant-…)\n"+
			"  --api-key-command CMD    (e.g. 'op read op://vault/anthropic/key')\n"+
			"  export %s=sk-ant-…", EnvVar, EnvVar)
}

// runKeyCommand executes a credential helper and returns its trimmed stdout.
//
// Run through a shell so the documented forms (`op read …`, `vault kv get -field=key …`) work
// verbatim, and bounded so a helper waiting on an interactive unlock prompt fails with a clear
// timeout rather than hanging a verification run indefinitely.
func runKeyCommand(cmdline string) (string, error) {
	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Stdin = nil
	var out strings.Builder
	var errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("api key command failed to start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("api key command failed: %w: %s", err, strings.TrimSpace(errOut.String()))
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("api key command timed out after 30s; if it prompts interactively, unlock first")
	}

	value := strings.TrimSpace(out.String())
	if value == "" {
		return "", fmt.Errorf("api key command produced no output")
	}
	// A multi-line result is almost always an error message or a stray banner rather than a
	// key, and passing it through would surface as a confusing auth failure inside the sandbox.
	if strings.ContainsAny(value, "\n\r") {
		return "", fmt.Errorf("api key command produced multiple lines; it should print only the key")
	}
	return value, nil
}
