package dashboard

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const accessTestAddr = "127.0.0.1:8765"

// accessServer serves the handler ListenAndServe serves, with a launch secret.
func accessServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	path := lensFixture(t)
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := servedHandler(Options{Addr: accessTestAddr, UserMode: true, LogPath: path, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, token
}

func noRedirectClient(jar http.CookieJar) *http.Client {
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func get(t *testing.T, client *http.Client, rawURL string) (*http.Response, string) {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestNewTokenIsLongAndFresh(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b || len(a) < 43 { // 32 bytes, unpadded base64url
		t.Fatalf("tokens %q and %q", a, b)
	}
}

func TestServedHandlerRequiresAToken(t *testing.T) {
	if _, err := servedHandler(Options{Addr: accessTestAddr, UserMode: true, LogPath: lensFixture(t)}); !errors.Is(err, errNoToken) {
		t.Fatalf("servedHandler without a token = %v", err)
	}
	if err := ListenAndServe(Options{Addr: "127.0.0.1:0", UserMode: true}); !errors.Is(err, errNoToken) {
		t.Fatalf("ListenAndServe without a token = %v", err)
	}
}

func TestListenAndServeAnnouncesOnlyAfterThePortIsBound(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	announced := false
	err = ListenAndServe(Options{
		Addr: busy.Addr().String(), UserMode: true, Token: token,
		Ready: func(net.Addr) error { announced = true; return nil },
	})
	if err == nil {
		t.Fatal("ListenAndServe bound a port that was already taken")
	}
	if announced {
		t.Fatal("Ready ran even though the listen failed")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- ListenAndServe(Options{
			Addr: "127.0.0.1:0", UserMode: true, Token: token,
			Ready: func(addr net.Addr) error {
				conn, dialErr := net.Dial("tcp", addr.String())
				if dialErr != nil {
					return dialErr
				}
				conn.Close()
				return errors.New("stop after bind")
			},
		})
	}()
	select {
	case err := <-errCh:
		if err == nil || err.Error() != "stop after bind" {
			t.Fatalf("ListenAndServe = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ready was not called after the port bound")
	}
}

func TestDashboardRefusesRequestsWithoutTheLaunchSecret(t *testing.T) {
	srv, token := accessServer(t)
	client := noRedirectClient(nil)
	for _, path := range []string{"/", "/index.html", "/session.html?id=s1", "/api/events", "/api/lens-data?session=s1", "/lenses/frame/activity", "/api/status"} {
		resp, body := get(t, client, srv.URL+path)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "printed") {
			t.Fatalf("%s without a token = %d %q", path, resp.StatusCode, body)
		}
	}
	// A wrong token, a wrong cookie, and an empty one.
	resp, body := get(t, client, srv.URL+"/?token=nope")
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Set-Cookie") != "" || !strings.Contains(body, "printed") {
		t.Fatalf("wrong token = %d %q, cookie %q", resp.StatusCode, body, resp.Header.Get("Set-Cookie"))
	}
	if resp, _ := get(t, client, srv.URL+"/?token="); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("empty token = %d", resp.StatusCode)
	}
	for _, value := range []string{"nope", "", token + "x"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
		req.AddCookie(&http.Cookie{Name: CookieName(accessTestAddr), Value: value})
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cookie %q = %d", value, resp.StatusCode)
		}
	}
	// The right secret in another port's cookie does not count.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	req.AddCookie(&http.Cookie{Name: CookieName("127.0.0.1:9999"), Value: token})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("another port's cookie = %d", resp.StatusCode)
	}
	// The loopback Host check still runs first.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/?token="+token, nil)
	req.Host = "attacker.example"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("rebinding host = %d, cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
}

func TestLaunchURLSetsTheCookieAndRedirectsWithoutTheToken(t *testing.T) {
	srv, token := accessServer(t)
	jar, _ := cookiejar.New(nil)
	client := noRedirectClient(jar)

	launch := LaunchURL(strings.TrimPrefix(srv.URL, "http://"), token, "/session.html?id=s1&lens=activity")
	if !strings.Contains(launch, "token="+token) || !strings.Contains(launch, "id=s1") || !strings.HasPrefix(launch, srv.URL+"/session.html?") {
		t.Fatalf("launch URL = %q", launch)
	}
	resp, _ := get(t, client, launch)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("launch status = %d", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || location.Path != "/session.html" || location.Query().Get("id") != "s1" || location.Query().Get("lens") != "activity" || location.Query().Has(TokenParam) {
		t.Fatalf("redirect = %q", resp.Header.Get("Location"))
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("redirect headers = %v", resp.Header)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName(accessTestAddr) {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != token || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("cookie = %+v", cookie)
	}

	// With the cookie: the session page, its lens data and the lens frame all load.
	for _, path := range []string{"/", "/session.html?id=s1", "/api/lens-data?session=s1", "/api/lenses", "/api/events"} {
		if resp, body := get(t, client, srv.URL+path); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s with the cookie = %d %q", path, resp.StatusCode, body)
		}
	}
	resp, _ = get(t, client, srv.URL+"/lenses/frame/activity")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox allow-scripts") || resp.Header.Get("X-Frame-Options") != "" {
		t.Fatalf("lens frame with the cookie = %d, headers %v", resp.StatusCode, resp.Header)
	}

	// A launch URL with nothing else redirects to the bare page.
	resp, _ = get(t, client, LaunchURL(strings.TrimPrefix(srv.URL, "http://"), token, ""))
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("bare launch = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestTokenRedirectStaysOnThisOrigin(t *testing.T) {
	if got := withoutToken("//attacker.example/x", url.Values{}); got != "/" {
		t.Fatalf("//host = %q", got)
	}
	if got := withoutToken("/\\attacker.example", url.Values{"a": {"b"}}); got != "/?a=b" {
		t.Fatalf("/\\host = %q", got)
	}
	if got := withoutToken("/session.html", url.Values{"id": {"s1"}}); got != "/session.html?id=s1" {
		t.Fatalf("path = %q", got)
	}
}

func TestCookieNameCarriesThePort(t *testing.T) {
	if CookieName("127.0.0.1:8765") == CookieName("127.0.0.1:8766") {
		t.Fatal("two ports share one cookie name")
	}
	if got := CookieName("[::1]:8765"); got != "beacon_dashboard_8765" {
		t.Fatalf("CookieName = %q", got)
	}
}
