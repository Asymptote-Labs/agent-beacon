package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// TokenParam is the query parameter that carries a launch's secret in the URL the command prints.
const TokenParam = "token"

// tokenBytes is the size of a launch secret: 256 bits from crypto/rand.
const tokenBytes = 32

// NewToken returns a fresh launch secret. Every `beacon endpoint dashboard` (and `beacon lenses
// preview`) run makes its own and keeps it only in memory and in the URL it prints, so another
// account on the machine that can reach the loopback port still cannot read the runtime log
// through it.
func NewToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// LaunchURL is the URL that opens the dashboard at addr with its launch secret. path is the page to
// open ("" or "/" for the activity page) and may carry its own query.
func LaunchURL(addr, token, path string) string {
	if path == "" {
		path = "/"
	}
	base, query, _ := strings.Cut(path, "?")
	values, _ := url.ParseQuery(query)
	values.Set(TokenParam, token)
	return strings.TrimRight(URL(addr), "/") + base + "?" + values.Encode()
}

// CookieName is the cookie a browser keeps a launch's secret in once the launch URL is opened.
// Browsers share cookies between the ports of one host, so the name carries the port: two
// dashboards on different ports (a lens preview beside the dashboard) then keep a cookie each.
func CookieName(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "default"
	}
	return "beacon_dashboard_" + port
}

// RequireToken lets a request through only when it carries the launch secret, as the token query
// parameter or the cookie a valid token sets.
//
// A GET or HEAD with a valid token gets the cookie (HttpOnly, SameSite=Strict, Path=/) and a
// redirect to the same page without the token, so the secret leaves the address bar and is never
// sent on as a Referer. Every later request, including the session page's API fetches and the
// lens frames it loads (same-site navigations), carries the cookie. Comparisons are constant time.
// A lens frame's own subresource requests come from an opaque origin, are cross-site for cookies
// and so carry no cookie, on top of the lens CSP that refuses them.
func RequireToken(token, cookieName string, next http.Handler) http.Handler {
	want := []byte(token)
	valid := func(got string) bool {
		return len(want) > 0 && subtle.ConstantTimeCompare([]byte(got), want) == 1
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Has(TokenParam) {
			if !valid(query.Get(TokenParam)) {
				refuseAccess(w, http.StatusForbidden, "This dashboard link is not valid for the running dashboard (it may be from an earlier run). Open the URL that `beacon endpoint dashboard` printed when it started.")
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				query.Del(TokenParam)
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "no-referrer")
				http.Redirect(w, r, withoutToken(r.URL.EscapedPath(), query), http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if cookie, err := r.Cookie(cookieName); err == nil && valid(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}
		refuseAccess(w, http.StatusUnauthorized, "This Beacon dashboard needs the link it printed when it started. Open the URL that `beacon endpoint dashboard` printed (it ends in ?token=...).")
	})
}

// withoutToken is the same-origin redirect target for path and what remains of its query. A path
// that a browser could read as another host ("//host", "/\host") becomes "/".
func withoutToken(path string, query url.Values) string {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		path = "/"
	}
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func refuseAccess(w http.ResponseWriter, status int, message string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}

// errNoToken is returned by ListenAndServe without a launch secret: the dashboard never serves the
// runtime log to whoever reaches its port.
var errNoToken = errors.New("dashboard: a launch token is required (dashboard.NewToken)")
