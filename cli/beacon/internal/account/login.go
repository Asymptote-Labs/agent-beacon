package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
)

const (
	DefaultBaseURL = "https://beacon.sh"
	BaseURLEnv     = "BEACON_AUTH_URL"
	LoginPagePath  = "/cli/auth"
	LoginInitPath  = "/api/cli/auth/init"
	ExchangePath   = "/api/cli/auth/exchange"
	RevokePath     = "/api/cli/auth/revoke"
	defaultTimeout = 30 * time.Second
	// LoginWait bounds how long Login waits for the browser. Exported so a caller
	// rendering its own waiting state can show the same deadline.
	LoginWait         = 5 * time.Minute
	CLIClientName     = "beacon-cli"
	CLIGrantVersion   = "2026-09-01"
	ScopeProfileRead  = "profile:read"
	ScopeDeviceEnroll = "device:enroll"
)

type LoginOptions struct {
	BaseURL     string
	Version     string
	NoBrowser   bool
	OpenBrowser func(string) error
	HTTPClient  *http.Client
	Out         io.Writer
	Timeout     time.Duration
	Now         func() time.Time
	// OnPrompt receives the sign-in URL once the callback server is listening and
	// the session is registered, and again if opening a browser then failed.
	//
	// When it is set, Login writes nothing to Out: a caller rendering its own UI
	// owns the screen, and stray writes would corrupt an alternate-screen frame.
	OnPrompt func(LoginPrompt)
}

// LoginPrompt is everything a user needs in order to finish signing in.
//
// It is reported before the browser is opened rather than after, so the URL is on
// screen even when the open fails or the terminal cannot open one at all. The
// callback server is already listening by then, so waiting never depends on the
// user having acknowledged anything -- the failure mode where a CLI only starts
// listening after a keypress is what this ordering avoids.
type LoginPrompt struct {
	// URL is the sign-in page, carrying the loopback port this process listens on.
	URL string
	// WillOpen is true when Login is about to try opening a browser itself.
	WillOpen bool
	// BrowserErr is set on the second call when that attempt failed; the URL is then
	// the only way through.
	BrowserErr error
	// Timeout is how long Login will wait before giving up.
	Timeout time.Duration
	// Port is the loopback port the sign-in redirects to. Signing in from another computer over
	// SSH means forwarding it: ssh -L Port:127.0.0.1:Port.
	Port int
}

type clientInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	GrantVersion string `json:"grant_version"`
}

type loginInitRequest struct {
	CodeChallenge       string     `json:"code_challenge"`
	CodeChallengeMethod string     `json:"code_challenge_method"`
	State               string     `json:"state"`
	RedirectPort        int        `json:"redirect_port"`
	Client              clientInfo `json:"client"`
	Scopes              []string   `json:"scopes"`
}

type exchangeRequest struct {
	ExchangeCode string `json:"exchange_code"`
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
}

type exchangeResult struct {
	AccessToken        string         `json:"access_token"`
	TokenType          string         `json:"token_type"`
	ExpiresAt          time.Time      `json:"expires_at"`
	User               User           `json:"user"`
	Organizations      []Organization `json:"organizations,omitempty"`
	ActiveOrganization *Organization  `json:"active_organization,omitempty"`
	Scopes             []string       `json:"scopes,omitempty"`
}

func Login(ctx context.Context, opts LoginOptions) (*Session, error) {
	baseURL := ResolveBaseURL(opts.BaseURL)
	if !secureURL(baseURL) {
		return nil, fmt.Errorf("Beacon auth URL must use https://: %s", baseURL)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	openBrowser := opts.OpenBrowser
	if openBrowser == nil {
		openBrowser = auth.OpenBrowser
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = LoginWait
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	pkce, err := auth.GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE parameters: %w", err)
	}

	var exchanged *exchangeResult
	exchange := func(ctx context.Context, exchangeCode, state, codeVerifier string) error {
		var result exchangeResult
		if err := auth.PostJSON(ctx, client, baseURL+ExchangePath, exchangeRequest{
			ExchangeCode: exchangeCode,
			State:        state,
			CodeVerifier: codeVerifier,
		}, &result); err != nil {
			return err
		}
		if err := validateExchange(&result, now()); err != nil {
			return err
		}
		exchanged = &result
		return nil
	}
	callback, err := auth.NewCallbackServer(pkce.State, pkce.CodeVerifier, exchange, client.Timeout)
	if err != nil {
		return nil, err
	}
	callback.SetSuccessPage("Signed in to Beacon", "Return to your terminal to finish setting up this machine.")
	defer callback.Shutdown()
	callback.Start()

	if err := auth.PostJSON(ctx, client, baseURL+LoginInitPath, loginInitRequest{
		CodeChallenge:       pkce.CodeChallenge,
		CodeChallengeMethod: "S256",
		State:               pkce.State,
		RedirectPort:        callback.Port(),
		Client: clientInfo{
			Name:         CLIClientName,
			Version:      opts.Version,
			OS:           runtime.GOOS,
			Arch:         runtime.GOARCH,
			GrantVersion: CLIGrantVersion,
		},
		Scopes: []string{ScopeProfileRead, ScopeDeviceEnroll},
	}, nil); err != nil {
		return nil, fmt.Errorf("start Beacon sign-in: %w", err)
	}

	port := callback.Port()
	loginURL := buildLoginURL(baseURL, pkce.State, port)
	report := opts.OnPrompt
	if report != nil {
		out = io.Discard
	} else {
		report = func(LoginPrompt) {}
	}
	report(LoginPrompt{URL: loginURL, WillOpen: !opts.NoBrowser, Timeout: timeout, Port: port})
	if opts.NoBrowser {
		printLoginURL(out, loginURL, port)
	} else if err := openBrowser(loginURL); err != nil {
		report(LoginPrompt{URL: loginURL, BrowserErr: err, Timeout: timeout, Port: port})
		if errors.Is(err, auth.ErrNoDisplay) {
			// Said at once, not after the wait times out: the redirect has to land on this
			// machine, so the person needs the URL and the port to forward, not a spinner.
			msg := err.Error()
			fmt.Fprintf(out, "%s%s.\n", strings.ToUpper(msg[:1]), msg[1:])
			fmt.Fprintln(out, "Sign-in finishes when beacon.sh redirects a browser back to this machine.")
		} else {
			fmt.Fprintln(out, "Could not open a browser automatically.")
		}
		printLoginURL(out, loginURL, port)
	} else {
		fmt.Fprintf(out, "Opening %s in your browser...\n", baseURL)
	}
	fmt.Fprintln(out, "Waiting for Beacon sign-in...")

	result, err := callback.WaitContext(ctx, timeout)
	if err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("Beacon sign-in failed: %s", result.Error)
	}
	if exchanged == nil {
		return nil, errors.New("Beacon sign-in finished without a session")
	}
	return &Session{
		SchemaVersion:      SchemaVersion,
		BaseURL:            baseURL,
		AccessToken:        exchanged.AccessToken,
		TokenType:          firstNonEmpty(exchanged.TokenType, "Bearer"),
		ExpiresAt:          exchanged.ExpiresAt,
		CreatedAt:          now().UTC(),
		User:               exchanged.User,
		Organizations:      exchanged.Organizations,
		ActiveOrganization: exchanged.ActiveOrganization,
		Scopes:             exchanged.Scopes,
	}, nil
}

// Revoke asks beacon.sh to invalidate the CLI credential. Callers should remove
// the local session regardless of this result so logout still works offline.
func Revoke(ctx context.Context, session Session, client *http.Client) error {
	if !secureURL(session.BaseURL) {
		return errors.New("refusing to send the Beacon credential to an insecure URL")
	}
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	body, _ := json.Marshal(map[string]string{"grant_version": CLIGrantVersion})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(session.BaseURL, "/")+RevokePath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", firstNonEmpty(session.TokenType, "Bearer")+" "+session.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", CLIClientName)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	var payload struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
	detail := firstNonEmpty(payload.Detail, payload.Error)
	if detail != "" {
		return errors.New(detail)
	}
	return fmt.Errorf("revoke request returned HTTP %d", resp.StatusCode)
}

func ResolveBaseURL(flagValue string) string {
	value := strings.TrimSpace(flagValue)
	if value == "" {
		value = strings.TrimSpace(os.Getenv(BaseURLEnv))
	}
	if value == "" {
		value = DefaultBaseURL
	}
	return strings.TrimRight(value, "/")
}

// printLoginURL shows the sign-in URL and how to use it from another computer.
func printLoginURL(out io.Writer, loginURL string, port int) {
	fmt.Fprintf(out, "Open this URL to sign in to Beacon:\n%s\n", loginURL)
	fmt.Fprintf(out, "From another computer over SSH, forward the port first: %s\n", SSHForwardCommand(port))
}

// SSHForwardCommand is the port forward that lets a browser on the SSH client finish a sign-in
// that redirects to port on this machine.
func SSHForwardCommand(port int) string {
	return fmt.Sprintf("ssh -L %d:127.0.0.1:%d <this machine>", port, port)
}

func buildLoginURL(baseURL, state string, port int) string {
	u, err := url.Parse(baseURL + LoginPagePath)
	if err != nil {
		return baseURL + LoginPagePath
	}
	query := u.Query()
	query.Set("state", state)
	query.Set("port", fmt.Sprint(port))
	u.RawQuery = query.Encode()
	return u.String()
}

func secureURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func validateExchange(result *exchangeResult, now time.Time) error {
	switch {
	case result.AccessToken == "":
		return errors.New("exchange response did not include an access token")
	case result.TokenType != "" && !strings.EqualFold(result.TokenType, "Bearer"):
		return fmt.Errorf("exchange response returned unsupported token type %q", result.TokenType)
	case result.User.ID == "" || result.User.Email == "":
		return errors.New("exchange response did not include the signed-in user")
	case result.ExpiresAt.IsZero():
		return errors.New("exchange response did not include token expiry")
	case !result.ExpiresAt.After(now):
		return errors.New("exchange response returned an expired token")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
