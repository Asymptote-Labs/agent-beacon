package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultEndpoint is the signup endpoint compiled into released binaries. It is a var
// so a release build can retarget it with -ldflags.
//
// This is Asymptote's own hostname rather than the raw <project-ref>.supabase.co URL,
// which keeps the project reference out of every shipped binary. Note the limit of
// that: auth.asymptotelabs.ai is itself the project's Supabase custom domain, so it
// cannot be repointed at a different provider without moving auth too. If signups
// ever need to outlive this Supabase project, put a proxy on a dedicated hostname and
// change this constant then -- installs already in the wild will keep using whatever
// they shipped with.
var DefaultEndpoint = "https://auth.asymptotelabs.ai/functions/v1/beacon-signup"

// EndpointEnv overrides the endpoint. Used by tests and by anyone running their own
// collector for an internal rollout.
const EndpointEnv = "BEACON_SIGNUP_ENDPOINT"

const submitTimeout = 5 * time.Second

var httpClient = &http.Client{Timeout: submitTimeout}

// Submission is the payload sent to the signup endpoint.
//
// Every field is either something the user typed or something about the install
// itself. No prompt text, file path, repository name, or telemetry content is
// included, and the README documents this list verbatim.
type Submission struct {
	InstallID        string   `json:"install_id"`
	Email            string   `json:"email"`
	Usage            string   `json:"usage"`
	EmailDomain      string   `json:"email_domain"`
	EmailDomainKind  string   `json:"email_domain_kind"`
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	OSVersion        string   `json:"os_version,omitempty"`
	BeaconVersion    string   `json:"beacon_version"`
	InstallMethod    string   `json:"install_method"`
	InstallMode      string   `json:"install_mode"`
	DetectedRuntimes []string `json:"detected_runtimes"`
	Source           string   `json:"source"`
	ClientSentAt     string   `json:"client_sent_at"`
}

// Install method values.
const (
	MethodHomebrew = "homebrew"
	MethodPackage  = "package"
	MethodSource   = "source"
)

// SourceCLIOnboarding marks rows that came from this prompt, so the table can later
// hold signups from other surfaces without ambiguity.
const SourceCLIOnboarding = "cli_onboarding"

// Endpoint returns the signup endpoint for this process.
func Endpoint() string {
	if v := strings.TrimSpace(os.Getenv(EndpointEnv)); v != "" {
		return v
	}
	return DefaultEndpoint
}

// Submit posts the signup and reports which outcome to record.
//
// It returns an Outcome* constant alongside any error. Callers must treat the outcome
// as authoritative and the error as explanatory only: onboarding never fails an
// install, so a returned error is for logging, not for propagating.
//
//   - 2xx                    -> OutcomeSubmitted
//   - 4xx, including 429     -> OutcomeRejected (terminal; nothing useful to retry)
//   - 5xx, network, timeout  -> OutcomePending  (worth resending later)
func Submit(ctx context.Context, sub Submission) (string, error) {
	body, err := json.Marshal(sub)
	if err != nil {
		// A payload we cannot even marshal will never marshal later either.
		return OutcomeRejected, fmt.Errorf("encode signup: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint(), bytes.NewReader(body))
	if err != nil {
		return OutcomeRejected, fmt.Errorf("build signup request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "beacon/"+sub.BeaconVersion+" ("+sub.OS+"/"+sub.Arch+")")

	resp, err := httpClient.Do(req)
	if err != nil {
		return OutcomePending, fmt.Errorf("send signup: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return OutcomeSubmitted, nil
	case resp.StatusCode >= 500:
		return OutcomePending, fmt.Errorf("signup endpoint returned %s", resp.Status)
	default:
		return OutcomeRejected, fmt.Errorf("signup endpoint returned %s", resp.Status)
	}
}

// NewSubmission assembles a payload from the answers plus locally derived install
// context.
func NewSubmission(installID, email, usage, beaconVersion, installMode string, runtimes []string) Submission {
	domain := EmailDomain(email)
	if runtimes == nil {
		runtimes = []string{}
	}
	return Submission{
		InstallID:        installID,
		Email:            email,
		Usage:            usage,
		EmailDomain:      domain,
		EmailDomainKind:  ClassifyDomain(domain),
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		OSVersion:        OSVersion(),
		BeaconVersion:    beaconVersion,
		InstallMethod:    InstallMethod(),
		InstallMode:      installMode,
		DetectedRuntimes: runtimes,
		Source:           SourceCLIOnboarding,
		ClientSentAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// InstallMethod guesses how this binary got onto the machine from where it lives.
// Homebrew and the signed packages install to well-known prefixes; anything else is
// treated as a local build.
func InstallMethod() string {
	exe, err := os.Executable()
	if err != nil {
		return MethodSource
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	switch {
	case strings.Contains(exe, "/Cellar/"), strings.Contains(exe, "/homebrew/"), strings.Contains(exe, "/linuxbrew/"):
		return MethodHomebrew
	case strings.HasPrefix(exe, "/opt/beacon/"), strings.HasPrefix(exe, "/usr/local/beacon/"):
		return MethodPackage
	default:
		return MethodSource
	}
}

// OSVersion returns a short OS version string, or "" if it cannot be determined
// cheaply. This is never worth blocking an install on, so every failure path returns
// the empty string and the field is omitted from the payload.
func OSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	case "linux":
		return linuxOSVersion("/etc/os-release")
	default:
		return ""
	}
}

// linuxOSVersion reads a distribution name and version out of an os-release file.
// The path is a parameter so the parsing is testable without touching /etc.
func linuxOSVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		fields[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	id := fields["ID"]
	version := fields["VERSION_ID"]
	switch {
	case id != "" && version != "":
		return id + " " + version
	case fields["PRETTY_NAME"] != "":
		return fields["PRETTY_NAME"]
	default:
		return id
	}
}
