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
	successLabel string
	successHref  string
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

// SetSuccessLink adds a button to the success page, so the browser tab is not a
// dead end. The caller supplies a trusted https URL.
func (cs *CallbackServer) SetSuccessLink(label, href string) {
	cs.successLabel = label
	cs.successHref = href
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
		Title:     cs.successTitle,
		Heading:   cs.successTitle,
		Lead:      cs.successBody,
		Note:      "You can close this tab.",
		Link:      cs.successHref,
		LinkLabel: cs.successLabel,
	})
}

type callbackPage struct {
	Title   string
	Heading string
	Lead    string
	Note    string
	Failed  bool
	// Link and LinkLabel render a button under the lead when both are set.
	Link      string
	LinkLabel string
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
	var button string
	if p.Link != "" && p.LinkLabel != "" {
		button = fmt.Sprintf(`<p><a class="button" href="%s">%s</a></p>`, html.EscapeString(p.Link), html.EscapeString(p.LinkLabel))
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
    .brand {
      display: inline-flex; align-items: center; gap: 10px; margin-bottom: 28px;
      color: #e8e8ed; font-size: 17px; font-weight: 750; letter-spacing: -.04em;
    }
    .brand svg { display: block; width: 30px; height: 30px; }
    h1 { font-size: 22px; line-height: 1.3; margin: 0 0 12px; font-weight: 600; }
    .lead { margin: 0; color: #a8a8b3; }
    .note { margin: 28px 0 0; font-size: 13px; color: #74747f; }
    a { color: %s; }
    .button {
      display: inline-block; margin-top: 24px; padding: 10px 20px; border-radius: 8px;
      background: %s; color: #fff; font-weight: 600; text-decoration: none;
    }
    @media (prefers-color-scheme: light) {
      body { background: #fbfbfd; color: #16161a; }
      .brand { color: #16161a; }
      .lead { color: #5b5b66; }
      .note { color: #8a8a94; }
    }
  </style>
</head>
<body>
  <main class="card">
    <div class="brand" aria-label="Beacon">
      <svg viewBox="21 20 200 200" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
        <path d="M182.194 156.596C152.215 156.596 127.94 180.244 127.939 210.23V210.525H114.234V210.23H114.257C114.257 180.244 89.9597 156.596 60.0029 156.596V126.709C89.9595 126.709 114.256 150.357 114.257 180.343H114.234V180.639H127.939V180.343C127.94 150.357 152.215 126.709 182.194 126.709V156.596ZM127.96 30.2949H127.938C127.938 60.281 152.235 83.9295 182.191 83.9297V113.816C152.235 113.816 127.938 90.1685 127.938 60.1826H127.96V59.8867H114.255V60.1826C114.255 90.1687 89.9796 113.816 60 113.816V83.9297C89.9797 83.9297 114.255 60.2812 114.255 30.2949V30H127.96V30.2949Z" fill="#FF5400"/>
      </svg>
      <span>Beacon</span>
    </div>
    <h1>%s</h1>
    <p class="lead">%s</p>
    %s
    %s
  </main>
</body>
</html>`, html.EscapeString(p.Title), accent, accent,
		html.EscapeString(p.Heading), html.EscapeString(p.Lead), button, footer.String())
}
