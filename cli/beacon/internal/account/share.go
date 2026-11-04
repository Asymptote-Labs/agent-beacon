package account

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Shared traces: `beacon share` uploads one session to beacon.sh as a beacon.trace.v1 trace: its
// summary with `PUT /v1/traces/{external_id}`, its events with
// `POST /v1/traces/{external_id}/events/batch`, and reads its own trace back with
// `GET /v1/traces/{external_id}`. These are explicit, user-initiated requests; no
// hook or background job ever makes them.
//
// They authorize with the signed-in CLI token when it carries ScopeTracesWrite, or with a Beacon API
// key (bcn_api_...) that carries the same scope. Either credential goes only in the Authorization
// header of these requests, to the API of the service it belongs to (ShareAPIBaseURL: beacon.sh
// serves its API from api.beacon.sh), and redirects are refused so it can never be resent somewhere
// else.

const (
	// TracesPath is the prefix of the shared-trace write API.
	TracesPath = "/v1/traces/"
	// APIKeyEnv names the environment variable an API key is read from when there is no --key and no
	// usable sign-in.
	APIKeyEnv = "BEACON_API_KEY"
	// APIKeyPrefix starts every Beacon API key.
	APIKeyPrefix = "bcn_api_"
	// APIURLEnv names the environment variable that says where the shared-trace API is, for a
	// service whose API is neither at its sign-in URL nor at the host ShareAPIBaseURL knows for it.
	APIURLEnv = "BEACON_API_URL"
	// DefaultAPIURL is the API of DefaultBaseURL. The beacon.sh web host does not serve /v1.
	DefaultAPIURL = "https://api.beacon.sh"

	// ShareMaxBatchBytes bounds one batch body before compression. beacon.sh accepts 8 MiB; the
	// margin covers the batch envelope and JSON escaping differences.
	ShareMaxBatchBytes = 6 << 20
	// shareMaxResponseBytes bounds what is read of any response.
	shareMaxResponseBytes = 1 << 20
)

// Where a share credential came from.
const (
	CredentialSourceFlag  = "flag"
	CredentialSourceLogin = "login"
	CredentialSourceEnv   = "env"
)

// ErrTracesWriteScope means the signed-in session cannot share traces: it was issued before
// `beacon login` asked for ScopeTracesWrite, or beacon.sh refused that scope.
var ErrTracesWriteScope = errors.New("this Beacon sign-in cannot share traces: run `beacon login` again")

// ErrNoShareCredential means there is neither an API key nor a sign-in to share with.
var ErrNoShareCredential = errors.New("not signed in to Beacon: run `beacon login`, or pass an API key with --key or " + APIKeyEnv)

// ShareCredential is the bearer credential a ShareClient sends and the base URL it belongs to.
// Token is never printed or logged; String hides it.
type ShareCredential struct {
	BaseURL string
	Token   string
	// Source is CredentialSourceFlag, CredentialSourceLogin or CredentialSourceEnv.
	Source string
	// User is the signed-in user for a login credential; empty for an API key.
	User User
}

// String describes the credential without its secret.
func (c ShareCredential) String() string {
	switch c.Source {
	case CredentialSourceLogin:
		return "Beacon sign-in " + c.User.Email
	case CredentialSourceFlag:
		return "API key from --key"
	case CredentialSourceEnv:
		return "API key from " + APIKeyEnv
	}
	return "credential"
}

// GoString keeps %#v from printing the token.
func (c ShareCredential) GoString() string { return "account.ShareCredential{" + c.String() + "}" }

// ResolveShareCredentialOptions are the inputs ResolveShareCredential chooses between.
type ResolveShareCredentialOptions struct {
	// Key is the --key flag.
	Key string
	// BaseURL is the --base-url flag; an API key is sent to ResolveBaseURL(BaseURL).
	BaseURL string
	// Load reads the stored sign-in; nil means Load.
	Load func() (*Session, error)
	// Getenv reads the environment; nil means no environment.
	Getenv func(string) string
	Now    time.Time
}

// ResolveShareCredential picks the credential to share with: --key first, then a stored sign-in that
// is current and carries ScopeTracesWrite, then BEACON_API_KEY. When none is usable the error says
// why the sign-in could not be used, since that is what most people have.
func ResolveShareCredential(opts ResolveShareCredentialOptions) (ShareCredential, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if key := strings.TrimSpace(opts.Key); key != "" {
		return apiKeyCredential(key, opts.BaseURL, CredentialSourceFlag, "--key")
	}
	load := opts.Load
	if load == nil {
		load = Load
	}
	session, loadErr := load()
	var loginErr error
	switch {
	case errors.Is(loadErr, ErrNotSignedIn):
		loginErr = ErrNoShareCredential
	case loadErr != nil:
		loginErr = loadErr
	case session.Expired(opts.Now):
		loginErr = errors.New("Beacon sign-in expired: run `beacon login` again")
	case !hasScope(session.Scopes, ScopeTracesWrite):
		loginErr = ErrTracesWriteScope
	case explicitShareTarget(opts) != "" && !sameBaseURL(explicitShareTarget(opts), session.BaseURL):
		// The sign-in belongs to another service than the one named. Sending the trace to the
		// sign-in's service instead would publish it somewhere the person did not ask for.
		loginErr = fmt.Errorf("signed in to %s, not %s: run `beacon login --auth-url %s`, or pass an API key for it", strings.TrimRight(session.BaseURL, "/"), explicitShareTarget(opts), explicitShareTarget(opts))
	default:
		return ShareCredential{BaseURL: strings.TrimRight(session.BaseURL, "/"), Token: session.AccessToken, Source: CredentialSourceLogin, User: session.User}, nil
	}
	if opts.Getenv != nil {
		if key := strings.TrimSpace(opts.Getenv(APIKeyEnv)); key != "" {
			return apiKeyCredential(key, opts.BaseURL, CredentialSourceEnv, APIKeyEnv)
		}
	}
	return ShareCredential{}, loginErr
}

// explicitShareTarget is the service named by --auth-url or BEACON_AUTH_URL, or "" when neither is
// set and the default applies.
func explicitShareTarget(opts ResolveShareCredentialOptions) string {
	value := strings.TrimSpace(opts.BaseURL)
	if value == "" && opts.Getenv != nil {
		value = strings.TrimSpace(opts.Getenv(BaseURLEnv))
	}
	return strings.TrimRight(value, "/")
}

func sameBaseURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func apiKeyCredential(key, baseURL, source, origin string) (ShareCredential, error) {
	if !strings.HasPrefix(key, APIKeyPrefix) || len(key) == len(APIKeyPrefix) || strings.ContainsAny(key, " \t\r\n") {
		// The value is deliberately not echoed: it may be a secret pasted into the wrong flag.
		return ShareCredential{}, fmt.Errorf("%s is not a Beacon API key (they start with %s)", origin, APIKeyPrefix)
	}
	return ShareCredential{BaseURL: ResolveBaseURL(baseURL), Token: key, Source: source}, nil
}

// ShareAPIBaseURL is where the shared-trace API of the service at baseURL lives: the URL in
// BEACON_API_URL when it is set, https://api.beacon.sh for beacon.sh (whose web host answers /v1 with
// its own HTML 404 page), and otherwise baseURL itself, as a self-hosted or local service serves
// both. getenv may be nil.
func ShareAPIBaseURL(baseURL string, getenv func(string) string) string {
	if getenv != nil {
		if override := strings.TrimRight(strings.TrimSpace(getenv(APIURLEnv)), "/"); override != "" {
			return override
		}
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Scheme, "https") {
		switch strings.ToLower(u.Hostname()) {
		case "beacon.sh", "www.beacon.sh":
			return DefaultAPIURL
		}
	}
	return base
}

// ShareClient talks to the shared-trace write API.
type ShareClient struct {
	cred ShareCredential
	// apiBase is where the requests go: ShareAPIBaseURL of the credential's base URL, which stays
	// the service that issued the credential.
	apiBase string
	http    *http.Client
}

// NewShareClient checks the credential, its base URL and the API it is sent to before anything is
// sent. client may be nil.
func NewShareClient(cred ShareCredential, client *http.Client) (*ShareClient, error) {
	if !secureURL(cred.BaseURL) {
		return nil, errors.New("refusing to send the Beacon credential to an insecure URL")
	}
	apiBase := ShareAPIBaseURL(cred.BaseURL, os.Getenv)
	if !secureURL(apiBase) {
		return nil, fmt.Errorf("refusing to send the Beacon credential to an insecure URL (%s)", APIURLEnv)
	}
	if strings.TrimSpace(cred.Token) == "" {
		return nil, ErrNoShareCredential
	}
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	// A redirect would resend the credential somewhere else.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ShareClient{cred: cred, apiBase: apiBase, http: &clientCopy}, nil
}

// BaseURL is the API the client sends its requests to (ShareAPIBaseURL).
func (c *ShareClient) BaseURL() string { return c.apiBase }

// PutTrace creates or updates a trace's summary and sharing settings.
func (c *ShareClient) PutTrace(ctx context.Context, externalID string, trace asymptoteobserve.ShareTraceV1) (*asymptoteobserve.ShareTraceResultV1, error) {
	if trace.SchemaVersion == "" {
		trace.SchemaVersion = asymptoteobserve.TraceSchemaVersion
	}
	if trace.SchemaVersion != asymptoteobserve.TraceSchemaVersion {
		return nil, fmt.Errorf("unsupported trace schema %q", trace.SchemaVersion)
	}
	if strings.TrimSpace(trace.Summary.Harness.Name) == "" {
		return nil, errors.New("a shared trace needs the harness that recorded it")
	}
	if trace.Visibility != "" && !asymptoteobserve.ValidShareVisibility(trace.Visibility) {
		return nil, fmt.Errorf("unknown visibility %q", trace.Visibility)
	}
	body, err := json.Marshal(trace)
	if err != nil {
		return nil, err
	}
	payload, status, err := c.do(ctx, http.MethodPut, externalID, "", body, false)
	if err != nil {
		return nil, err
	}
	if err := c.statusError("share trace", status, payload); err != nil {
		return nil, err
	}
	var result asymptoteobserve.ShareTraceResultV1
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("parse share response: %w", err)
	}
	if result.ID == "" {
		return nil, errors.New("share response did not include a trace id")
	}
	return &result, nil
}

// PostEventsResult sums the batches PostEvents sent.
type PostEventsResult struct {
	Batches  int `json:"batches"`
	Upserted int `json:"upserted"`
	// EventCount is the trace's total after the last batch, as beacon.sh reported it.
	EventCount int `json:"event_count"`
}

// PostEvents upserts the batch's events in requests of at most ShareMaxEventsPerBatch events and
// ShareMaxBatchBytes bytes, each gzip-compressed. Events are upserted by id, so re-sending one is
// idempotent and a failed run can simply be repeated. An empty batch sends nothing.
func (c *ShareClient) PostEvents(ctx context.Context, externalID string, batch asymptoteobserve.ShareEventsBatchV1) (PostEventsResult, error) {
	var result PostEventsResult
	batches, err := ShareBatches(batch.Events, asymptoteobserve.ShareMaxEventsPerBatch, ShareMaxBatchBytes)
	if err != nil {
		return result, err
	}
	for _, request := range batches {
		payload, status, err := c.do(ctx, http.MethodPost, externalID, "/events/batch", request.Body, true)
		if err != nil {
			return result, err
		}
		if err := c.statusError("upload events", status, payload); err != nil {
			return result, err
		}
		var upserted asymptoteobserve.ShareBatchResultV1
		if err := json.Unmarshal(payload, &upserted); err != nil {
			return result, fmt.Errorf("parse batch response: %w", err)
		}
		result.Batches++
		result.Upserted += upserted.Upserted
		result.EventCount = upserted.EventCount
	}
	return result, nil
}

// GetTrace reads the owner's view of a trace, with the id of the last event beacon.sh holds. found
// is false when beacon.sh has no such trace for this credential.
func (c *ShareClient) GetTrace(ctx context.Context, externalID string) (*asymptoteobserve.ShareTraceStateV1, bool, error) {
	payload, status, err := c.do(ctx, http.MethodGet, externalID, "", nil, false)
	if err != nil {
		return nil, false, err
	}
	// Only the API's own JSON answer means the trace is not there yet. A 404 page from a host with no
	// trace API is an error: creating the trace there next would fail the same way.
	if status == http.StatusNotFound && jsonBody(payload) {
		return nil, false, nil
	}
	if err := c.statusError("read shared trace", status, payload); err != nil {
		return nil, false, err
	}
	var state asymptoteobserve.ShareTraceStateV1
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, false, fmt.Errorf("parse shared trace: %w", err)
	}
	return &state, true, nil
}

// ShareBatch is one batch request body and how many events it carries.
type ShareBatch struct {
	Body   []byte
	Events int
}

// ShareBatches splits events into ShareEventsBatchV1 bodies of at most maxEvents events and
// maxBytes bytes. A single event larger than maxBytes is refused rather than sent to be rejected.
func ShareBatches(events []asymptoteobserve.TraceEventV1, maxEvents, maxBytes int) ([]ShareBatch, error) {
	const envelope = len(`{"events":[]}`)
	var batches []ShareBatch
	var current [][]byte
	size := envelope
	flush := func() {
		if len(current) == 0 {
			return
		}
		body := make([]byte, 0, size)
		body = append(body, `{"events":[`...)
		for i, e := range current {
			if i > 0 {
				body = append(body, ',')
			}
			body = append(body, e...)
		}
		body = append(body, `]}`...)
		batches = append(batches, ShareBatch{Body: body, Events: len(current)})
		current, size = nil, envelope
	}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		if envelope+len(encoded) > maxBytes {
			return nil, fmt.Errorf("event %s is %d bytes, more than one batch may carry", event.ID, len(encoded))
		}
		extra := len(encoded)
		if len(current) > 0 {
			extra++ // the comma
		}
		if len(current) >= maxEvents || size+extra > maxBytes {
			flush()
			extra = len(encoded)
		}
		current = append(current, encoded)
		size += extra
	}
	flush()
	return batches, nil
}

func (c *ShareClient) do(ctx context.Context, method, externalID, suffix string, body []byte, compress bool) ([]byte, int, error) {
	if !asymptoteobserve.ValidShareExternalID(externalID) {
		return nil, 0, fmt.Errorf("invalid external id %q", externalID)
	}
	target := c.apiBase + TracesPath + url.PathEscape(externalID) + suffix
	var reader io.Reader
	encoding := ""
	if body != nil {
		if compress {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, err := zw.Write(body); err != nil {
				return nil, 0, err
			}
			if err := zw.Close(); err != nil {
				return nil, 0, err
			}
			reader = &buf
			encoding = "gzip"
		} else {
			reader = bytes.NewReader(body)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cred.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", CLIClientName)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, c.apiBase+TracesPath+"…", scrubURLError(err))
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, shareMaxResponseBytes))
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, resp.StatusCode, fmt.Errorf("%s answered with a redirect (HTTP %d); not following it with the credential", c.apiBase, resp.StatusCode)
	}
	return payload, resp.StatusCode, nil
}

// scrubURLError drops the request URL a *url.Error carries; the cause is what matters, and the URL
// is already in the message.
func scrubURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func (c *ShareClient) statusError(what string, status int, payload []byte) error {
	if status >= 200 && status <= 299 {
		return nil
	}
	var failure struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(payload, &failure)
	detail := firstNonEmpty(failure.Detail, failure.Error, fmt.Sprintf("HTTP %d", status))
	detail = strings.ReplaceAll(detail, c.cred.Token, "[REDACTED]")
	switch status {
	case http.StatusUnauthorized:
		if c.cred.Source == CredentialSourceLogin {
			return fmt.Errorf("Beacon rejected the sign-in (%s): run `beacon login` again", detail)
		}
		return fmt.Errorf("Beacon rejected the %s (%s)", c.cred, detail)
	case http.StatusForbidden:
		if c.cred.Source == CredentialSourceLogin {
			return ErrTracesWriteScope
		}
		return fmt.Errorf("the %s cannot share traces: it needs the %s scope (%s)", c.cred, ScopeTracesWrite, detail)
	case http.StatusNotFound:
		if !jsonBody(payload) {
			return fmt.Errorf("%s: %s has no trace API (HTTP 404: endpoint not found)", what, c.apiBase)
		}
		return fmt.Errorf("%s: the trace does not exist on %s (%s)", what, c.apiBase, detail)
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%s: beacon.sh refused the request as too large (%s)", what, detail)
	}
	return fmt.Errorf("%s: %s", what, detail)
}

// jsonBody reports whether a response body is JSON, as every answer of the trace API is. A web
// host's 404 page is HTML.
func jsonBody(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	return len(payload) > 0 && json.Valid(payload)
}
