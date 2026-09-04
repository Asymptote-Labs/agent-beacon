package auth

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	callbackReadTimeout       = 10 * time.Second
	defaultExchangeTimeout    = 30 * time.Second
	callbackWriteTimeoutSlack = 5 * time.Second
)

// ExchangeFunc redeems the one-time code the dashboard delivered to the callback.
// It runs inside the callback request, so the browser sees the outcome; the
// caller keeps whatever the exchange returned in its own closure.
type ExchangeFunc func(ctx context.Context, exchangeCode, state, codeVerifier string) error

// CallbackResult is what Wait returns: the state that completed the flow, or the
// error that ended it.
type CallbackResult struct {
	State string
	Error string
}

// CallbackServer is the loopback HTTP server the dashboard redirects to with the
// exchange code. It listens on an ephemeral 127.0.0.1 port, accepts exactly one
// valid callback, and runs the ExchangeFunc before answering the browser.
type CallbackServer struct {
	port         int
	listener     net.Listener
	server       *http.Server
	resultCh     chan *CallbackResult
	mu           sync.Mutex
	completed    bool
	state        string
	codeVerifier string
	exchange     ExchangeFunc
	successTitle string
	successBody  string
}

// NewCallbackServer binds the loopback listener. exchangeTimeout should match the
// timeout of the HTTP client the ExchangeFunc uses so the browser response is not
// cut off before the exchange finishes; zero uses a 30 s default.
func NewCallbackServer(expectedState, codeVerifier string, exchange ExchangeFunc, exchangeTimeout time.Duration) (*CallbackServer, error) {
	if exchange == nil {
		return nil, fmt.Errorf("callback server needs an exchange function")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	if exchangeTimeout <= 0 {
		exchangeTimeout = defaultExchangeTimeout
	}
	cs := &CallbackServer{
		port:         listener.Addr().(*net.TCPAddr).Port,
		listener:     listener,
		resultCh:     make(chan *CallbackResult, 1),
		state:        expectedState,
		codeVerifier: codeVerifier,
		exchange:     exchange,
		successTitle: "Beacon Authentication Complete",
		successBody:  "You can close this window and return to the terminal.",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)
	cs.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: callbackReadTimeout,
		ReadTimeout:       callbackReadTimeout,
		WriteTimeout:      exchangeTimeout + callbackWriteTimeoutSlack,
	}
	return cs, nil
}

// SetSuccessPage customizes what the browser shows after a successful exchange.
func (cs *CallbackServer) SetSuccessPage(title, body string) {
	if title != "" {
		cs.successTitle = title
	}
	if body != "" {
		cs.successBody = body
	}
}

// Port is the ephemeral loopback port the dashboard must redirect to.
func (cs *CallbackServer) Port() int {
	return cs.port
}

// Start serves in the background until Shutdown.
func (cs *CallbackServer) Start() {
	go func() {
		if err := cs.server.Serve(cs.listener); err != nil && err != http.ErrServerClosed {
			cs.resultCh <- &CallbackResult{Error: fmt.Sprintf("server error: %v", err)}
		}
	}()
}

// Wait blocks for the first completed callback or the timeout.
func (cs *CallbackServer) Wait(timeout time.Duration) (*CallbackResult, error) {
	select {
	case result := <-cs.resultCh:
		return result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for the browser to finish")
	}
}

// Shutdown stops the listener.
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
		cs.sendResponse(w, false, "Callback already handled")
		return
	}
	if err := cs.exchange(r.Context(), exchangeCode, state, cs.codeVerifier); err != nil {
		message := fmt.Sprintf("failed to exchange code: %v", err)
		cs.resultCh <- &CallbackResult{Error: message}
		cs.sendResponse(w, false, message)
		return
	}
	cs.resultCh <- &CallbackResult{State: state}
	cs.sendResponse(w, true, "")
}

func (cs *CallbackServer) finishCallback(w http.ResponseWriter, result *CallbackResult, success bool, errorMsg string) {
	if !cs.reserveCompletion() {
		cs.sendResponse(w, false, "Callback already handled")
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

func (cs *CallbackServer) sendResponse(w http.ResponseWriter, success bool, errorMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if success {
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>%s</title></head>
<body>
  <h1>%s</h1>
  <p>%s</p>
</body>
</html>`, html.EscapeString(cs.successTitle), html.EscapeString(cs.successTitle), html.EscapeString(cs.successBody))
		return
	}
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Beacon Authentication Failed</title></head>
<body>
  <h1>Authentication Failed</h1>
  <p>%s</p>
  <p>Please return to the terminal and run the command again.</p>
</body>
</html>`, html.EscapeString(errorMsg))
}
