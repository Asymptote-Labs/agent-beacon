package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	callbackReadTimeout       = 10 * time.Second
	defaultExchangeTimeout    = 30 * time.Second
	callbackWriteTimeoutSlack = 5 * time.Second
)

type CallbackResult struct {
	Token       string
	TokenPrefix string
	ExpiresAt   string
	UserID      string
	Email       string
	OrgID       string
	OrgName     string
	State       string
	Error       string
}

type exchangeCodeRequest struct {
	ExchangeCode string `json:"exchange_code"`
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
}

type exchangeCodeResponse struct {
	Token       string  `json:"token"`
	TokenPrefix string  `json:"token_prefix"`
	ExpiresAt   *string `json:"expires_at"`
	UserID      string  `json:"user_id"`
	Email       string  `json:"email"`
	OrgID       string  `json:"org_id"`
	OrgName     string  `json:"org_name"`
}

type CallbackServer struct {
	port         int
	listener     net.Listener
	server       *http.Server
	resultCh     chan *CallbackResult
	mu           sync.Mutex
	completed    bool
	state        string
	codeVerifier string
	dashboardURL string
	httpClient   *http.Client
}

func NewCallbackServer(expectedState, codeVerifier, dashboardURL string, httpClient *http.Client) (*CallbackServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultExchangeTimeout}
	}
	cs := &CallbackServer{
		port:         listener.Addr().(*net.TCPAddr).Port,
		listener:     listener,
		resultCh:     make(chan *CallbackResult, 1),
		state:        expectedState,
		codeVerifier: codeVerifier,
		dashboardURL: normalizeDashboardURL(dashboardURL),
		httpClient:   httpClient,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)
	cs.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: callbackReadTimeout,
		ReadTimeout:       callbackReadTimeout,
		WriteTimeout:      callbackWriteTimeout(httpClient),
	}
	return cs, nil
}

func callbackWriteTimeout(httpClient *http.Client) time.Duration {
	exchangeTimeout := defaultExchangeTimeout
	if httpClient != nil && httpClient.Timeout > exchangeTimeout {
		exchangeTimeout = httpClient.Timeout
	}
	return exchangeTimeout + callbackWriteTimeoutSlack
}

func (cs *CallbackServer) Port() int {
	return cs.port
}

func (cs *CallbackServer) Start() {
	go func() {
		if err := cs.server.Serve(cs.listener); err != nil && err != http.ErrServerClosed {
			cs.resultCh <- &CallbackResult{Error: fmt.Sprintf("server error: %v", err)}
		}
	}()
}

func (cs *CallbackServer) Wait(timeout time.Duration) (*CallbackResult, error) {
	select {
	case result := <-cs.resultCh:
		return result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for authentication callback")
	}
}

func (cs *CallbackServer) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cs.server.Shutdown(ctx)
}

func (cs *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	state := query.Get("state")
	if state != cs.state {
		cs.sendResponse(w, false, "Security error: state mismatch")
		return
	}
	if errMsg := query.Get("error"); errMsg != "" {
		cs.finishCallback(w, &CallbackResult{Error: errMsg}, false, errMsg)
		return
	}
	exchangeCode := query.Get("exchange_code")
	if exchangeCode == "" {
		cs.sendResponse(w, false, "No exchange code received")
		return
	}
	if !cs.reserveCompletion() {
		cs.sendResponse(w, false, "Authentication callback already handled")
		return
	}
	tokenResp, err := cs.exchangeCodeForToken(r.Context(), exchangeCode, state, cs.codeVerifier)
	if err != nil {
		message := fmt.Sprintf("failed to exchange code: %v", err)
		cs.resultCh <- &CallbackResult{Error: message}
		cs.sendResponse(w, false, message)
		return
	}
	result := &CallbackResult{
		Token:       tokenResp.Token,
		TokenPrefix: tokenResp.TokenPrefix,
		UserID:      tokenResp.UserID,
		Email:       tokenResp.Email,
		OrgID:       tokenResp.OrgID,
		OrgName:     tokenResp.OrgName,
		State:       state,
	}
	if tokenResp.ExpiresAt != nil {
		result.ExpiresAt = *tokenResp.ExpiresAt
	}
	cs.resultCh <- result
	cs.sendResponse(w, true, "")
}

func (cs *CallbackServer) finishCallback(w http.ResponseWriter, result *CallbackResult, success bool, errorMsg string) {
	if !cs.reserveCompletion() {
		cs.sendResponse(w, false, "Authentication callback already handled")
		return
	}
	cs.resultCh <- result
	cs.sendResponse(w, success, errorMsg)
}

func (cs *CallbackServer) reserveCompletion() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.completed {
		return false
	}
	cs.completed = true
	return true
}

func (cs *CallbackServer) exchangeCodeForToken(ctx context.Context, exchangeCode, state, codeVerifier string) (*exchangeCodeResponse, error) {
	bodyBytes, err := json.Marshal(exchangeCodeRequest{
		ExchangeCode: exchangeCode,
		State:        state,
		CodeVerifier: codeVerifier,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cs.dashboardURL+"/api/cli/auth/exchange", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beacon-cli")

	resp, err := cs.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call exchange endpoint: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read exchange response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Detail string `json:"detail"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if errResp.Detail != "" {
				return nil, fmt.Errorf("%s", errResp.Detail)
			}
			if errResp.Error != "" {
				return nil, fmt.Errorf("%s", errResp.Error)
			}
		}
		return nil, fmt.Errorf("exchange failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var tokenResp exchangeCodeResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse exchange response: %w", err)
	}
	return &tokenResp, nil
}

func (cs *CallbackServer) sendResponse(w http.ResponseWriter, success bool, errorMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if success {
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Beacon Login Complete</title></head>
<body>
  <h1>Authentication Successful</h1>
  <p>You can close this window and return to the terminal.</p>
</body>
</html>`))
		return
	}
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Beacon Login Failed</title></head>
<body>
  <h1>Authentication Failed</h1>
  <p>%s</p>
  <p>Please return to the terminal and try <code>beacon login</code> again.</p>
</body>
</html>`, html.EscapeString(errorMsg))
}
