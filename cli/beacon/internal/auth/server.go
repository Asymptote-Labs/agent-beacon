package auth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	callbackReadTimeout = 10 * time.Second
	// defaultRedirectAfter is long enough to read two short lines and still short
	// enough that nobody waits on it.
	defaultRedirectAfter      = 6 * time.Second
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
		successTitle: "Signed in to Beacon",
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
	return cs.WaitContext(context.Background(), timeout)
}

// WaitContext is Wait that also returns when ctx is done.
//
// A caller rendering a cancellable waiting state needs to stop waiting without
// killing the command: onboarding lets the user abandon a browser sign-in and
// return to the wizard. The caller's deferred Shutdown releases the loopback
// listener either way.
func (cs *CallbackServer) WaitContext(ctx context.Context, timeout time.Duration) (*CallbackResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-cs.resultCh:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
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

// ErrInvalidPastedCallback reports pasted text that is not a callback for this
// sign-in. Nothing was consumed, so the caller can ask again.
var ErrInvalidPastedCallback = errors.New("that is not the sign-in address for this terminal")

// CompletePasted finishes the flow from text the user pasted instead of from a
// browser request: the full callback address the browser failed to load
// (http://127.0.0.1:<port>/callback?state=...&exchange_code=...), or the bare
// exchange code.
//
// It exists for signing in on a remote or headless machine, where the browser
// runs on another computer whose 127.0.0.1 is not this one. That is safe to
// accept by hand because the exchange code is useless without the PKCE verifier,
// which never leaves this process.
//
// Input that is not a callback for this sign-in returns an error wrapping
// ErrInvalidPastedCallback and leaves the flow open. Any other error means the
// flow ended, and Wait returns it too.
func (cs *CallbackServer) CompletePasted(ctx context.Context, input string) error {
	input = strings.Trim(strings.TrimSpace(input), `"'<>`)
	if input == "" {
		return fmt.Errorf("%w: nothing was pasted", ErrInvalidPastedCallback)
	}
	state, exchangeCode, errMsg := cs.state, input, ""
	if strings.Contains(input, "://") || strings.ContainsAny(input, "?&=") {
		parsed, err := url.Parse(input)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPastedCallback, err)
		}
		query := parsed.Query()
		state, exchangeCode, errMsg = query.Get("state"), query.Get("exchange_code"), query.Get("error")
		if state != cs.state {
			return fmt.Errorf("%w: it belongs to a different sign-in", ErrInvalidPastedCallback)
		}
		if exchangeCode == "" && errMsg == "" {
			return fmt.Errorf("%w: it has no exchange code", ErrInvalidPastedCallback)
		}
	} else if strings.ContainsAny(input, " \t/") {
		return fmt.Errorf("%w: paste the whole address from the browser's address bar", ErrInvalidPastedCallback)
	}
	if !cs.reserveCompletion() {
		return errors.New("sign-in already finished")
	}
	if errMsg != "" {
		cs.resultCh <- &CallbackResult{Error: errMsg}
		return errors.New(errMsg)
	}
	if err := cs.exchange(ctx, exchangeCode, state, cs.codeVerifier); err != nil {
		message := fmt.Sprintf("failed to exchange code: %v", err)
		cs.resultCh <- &CallbackResult{Error: message}
		return errors.New(message)
	}
	cs.resultCh <- &CallbackResult{State: state}
	return nil
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
		_, _ = fmt.Fprint(w, cs.successHTML())
		return
	}
	_, _ = fmt.Fprint(w, renderCallbackPage(callbackPage{
		Title:   "Beacon: sign-in failed",
		Heading: "Sign-in failed",
		Lead:    errorMsg,
		Note:    "Return to your terminal and run the command again.",
		Failed:  true,
	}))
}

func (cs *CallbackServer) successHTML() string {
	return renderCallbackPage(callbackPage{
		Title:   cs.successTitle,
		Heading: cs.successTitle,
		Lead:    cs.successBody,
		Note:    "You can close this tab.",
	})
}

type callbackPage struct {
	Title   string
	Heading string
	Lead    string
	Note    string
	Failed  bool
}

// renderCallbackPage draws the one page a browser ever sees from the CLI.
//
// Everything is inline. This is served from a loopback port with no network
// guarantee behind it, so a stylesheet or font fetched from anywhere else would be
// the one thing on screen that could fail.
func renderCallbackPage(p callbackPage) string {
	accent := "#7c5cff"
	if p.Failed {
		accent = "#e5484d"
	}
	var footer strings.Builder
	if p.Note != "" {
		fmt.Fprintf(&footer, `<p class="note">%s</p>`, html.EscapeString(p.Note))
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    :root { color-scheme: light dark; }
    * { box-sizing: border-box; }
    body {
      margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
      padding: 24px; background: #0b0b0f; color: #e8e8ed;
      font: 15px/1.6 ui-sans-serif, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    }
    .card { width: 100%%; max-width: 420px; text-align: center; }
    .mark { font-size: 11px; letter-spacing: .38em; color: %s; font-weight: 700; margin-bottom: 28px; }
    h1 { font-size: 22px; line-height: 1.3; margin: 0 0 12px; font-weight: 600; }
    .lead { margin: 0; color: #a8a8b3; }
    .note { margin: 28px 0 0; font-size: 13px; color: #74747f; }
    a { color: %s; }
    @media (prefers-color-scheme: light) {
      body { background: #fbfbfd; color: #16161a; }
      .lead { color: #5b5b66; }
      .note { color: #8a8a94; }
    }
  </style>
</head>
<body>
  <main class="card">
    <div class="mark">B E A C O N</div>
    <h1>%s</h1>
    <p class="lead">%s</p>
    %s
  </main>
</body>
</html>`, html.EscapeString(p.Title), accent, accent,
		html.EscapeString(p.Heading), html.EscapeString(p.Lead), footer.String())
}
