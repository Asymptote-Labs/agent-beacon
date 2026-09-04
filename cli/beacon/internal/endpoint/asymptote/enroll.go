package asymptote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
)

// Dashboard paths for device enrollment. The page and the two API routes are served by the
// Asymptote dashboard, which proxies to the platform API.
const (
	EnrollPagePath     = "/cli/enroll"
	EnrollInitPath     = "/api/cli/enroll/init"
	EnrollExchangePath = "/api/cli/enroll/exchange"
)

// DeviceInfo is what the CLI tells the dashboard about this machine. It is shown on the
// approval page and stored on the device record; none of it is secret.
type DeviceInfo struct {
	InstallID     string `json:"install_id"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	BeaconVersion string `json:"beacon_version"`
	InstallMode   string `json:"install_mode"`
}

// EnrollOptions configures one enrollment round trip.
type EnrollOptions struct {
	DashboardURL string
	Device       DeviceInfo
	// OpenBrowser opens the approval page; nil uses the platform default. Callers running as
	// root pass one that opens the browser as the console user.
	OpenBrowser func(string) error
	// NoBrowser prints the URL instead of opening anything.
	NoBrowser  bool
	HTTPClient *http.Client
	Out        io.Writer
	Timeout    time.Duration
}

// EnrollResult is the exchange response. DeviceKey is shown to nobody and written only to
// the secrets file.
type EnrollResult struct {
	DeviceID         string   `json:"device_id"`
	DeviceKey        string   `json:"device_key"`
	KeyPrefix        string   `json:"key_prefix"`
	IngestURL        string   `json:"ingest_url"`
	OrganizationID   string   `json:"organization_id"`
	OrganizationName string   `json:"organization_name"`
	Email            string   `json:"email"`
	Scopes           []string `json:"scopes"`
	ExpiresAt        *string  `json:"expires_at"`
}

type enrollInitRequest struct {
	CodeChallenge       string     `json:"code_challenge"`
	CodeChallengeMethod string     `json:"code_challenge_method"`
	State               string     `json:"state"`
	RedirectPort        int        `json:"redirect_port"`
	Device              DeviceInfo `json:"device"`
}

type enrollExchangeRequest struct {
	ExchangeCode string `json:"exchange_code"`
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
}

// Enroll runs the browser approval flow and returns the minted device key.
//
// Order matters for safety: the session is registered with the dashboard first (so the
// approval page can show the device details), the browser is opened to a URL that carries
// only the state and the loopback port, and the key is minted only when the callback's
// one-time code is exchanged together with the PKCE verifier that never left this process.
func Enroll(ctx context.Context, opts EnrollOptions) (*EnrollResult, error) {
	dashboardURL := auth.ResolveDashboardURL(opts.DashboardURL)
	if !strings.HasPrefix(strings.ToLower(dashboardURL), "https://") && !isLoopback(dashboardURL) {
		return nil, fmt.Errorf("dashboard URL must use https://: %s", dashboardURL)
	}
	if opts.Device.InstallID == "" {
		return nil, errors.New("enrollment needs an install id")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = auth.AuthTimeout
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	openBrowser := opts.OpenBrowser
	if openBrowser == nil {
		openBrowser = auth.OpenBrowser
	}

	pkce, err := auth.GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	var result *EnrollResult
	exchange := func(ctx context.Context, exchangeCode, state, codeVerifier string) error {
		var resp EnrollResult
		if err := auth.PostJSON(ctx, client, dashboardURL+EnrollExchangePath, enrollExchangeRequest{
			ExchangeCode: exchangeCode,
			State:        state,
			CodeVerifier: codeVerifier,
		}, &resp); err != nil {
			return err
		}
		if err := validateEnrollResult(&resp); err != nil {
			return err
		}
		result = &resp
		return nil
	}
	server, err := auth.NewCallbackServer(pkce.State, pkce.CodeVerifier, exchange, client.Timeout)
	if err != nil {
		return nil, err
	}
	server.SetSuccessPage("Beacon: device connected", "This machine is now forwarding to Asymptote. You can close this tab and return to the terminal.")
	defer func() { _ = server.Shutdown() }()
	server.Start()

	if err := auth.PostJSON(ctx, client, dashboardURL+EnrollInitPath, enrollInitRequest{
		CodeChallenge:       pkce.CodeChallenge,
		CodeChallengeMethod: "S256",
		State:               pkce.State,
		RedirectPort:        server.Port(),
		Device:              opts.Device,
	}, nil); err != nil {
		return nil, fmt.Errorf("could not start enrollment with %s: %w", dashboardURL, err)
	}

	approvalURL := buildEnrollURL(dashboardURL, pkce.State, server.Port())
	if opts.NoBrowser {
		fmt.Fprintf(out, "Open this URL in a browser where you are signed in to Asymptote:\n%s\n", approvalURL)
	} else {
		fmt.Fprintf(out, "Opening %s%s in your browser...\n", dashboardURL, EnrollPagePath)
		if err := openBrowser(approvalURL); err != nil {
			fmt.Fprintln(out, "Could not open a browser automatically.")
			fmt.Fprintf(out, "Open this URL in a browser where you are signed in to Asymptote:\n%s\n", approvalURL)
		}
	}
	fmt.Fprintln(out, "Waiting for a member of your organization to approve this device...")

	callback, err := server.Wait(timeout)
	if err != nil {
		return nil, err
	}
	if callback.Error != "" {
		return nil, fmt.Errorf("enrollment failed: %s", callback.Error)
	}
	if result == nil {
		return nil, errors.New("enrollment finished without a device key")
	}
	return result, nil
}

func validateEnrollResult(r *EnrollResult) error {
	switch {
	case r.DeviceKey == "" || !strings.HasPrefix(r.DeviceKey, "bcn_device_"):
		return errors.New("exchange response did not include a device key")
	case r.DeviceID == "" || r.OrganizationID == "":
		return errors.New("exchange response is missing the device or organization id")
	case !strings.HasPrefix(strings.ToLower(r.IngestURL), "https://") && !isLoopback(r.IngestURL):
		return fmt.Errorf("exchange response ingest URL must use https://: %q", r.IngestURL)
	}
	r.IngestURL = strings.TrimRight(r.IngestURL, "/")
	return nil
}

func buildEnrollURL(dashboardURL, state string, port int) string {
	u, err := url.Parse(dashboardURL + EnrollPagePath)
	if err != nil {
		return dashboardURL + EnrollPagePath
	}
	q := u.Query()
	q.Set("state", state)
	q.Set("port", fmt.Sprintf("%d", port))
	u.RawQuery = q.Encode()
	return u.String()
}

// isLoopback allows plain http only for local development servers.
func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
