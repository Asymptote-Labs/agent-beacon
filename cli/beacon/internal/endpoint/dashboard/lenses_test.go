package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// lensSpecDir locates spec/lenses from this file, so tests compare against what the spec publishes.
func lensSpecDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for dir := filepath.Dir(thisFile); ; {
		candidate := filepath.Join(dir, "spec", "lenses")
		if _, err := os.Stat(filepath.Join(candidate, "VERSION")); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate spec/lenses/VERSION")
		}
		dir = parent
	}
}

func lensTestHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := Handler(Options{UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serve(handler http.Handler, method, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, url, nil))
	return rec
}

// The activity built-in is the spec's example lens, byte for byte, so the example an agent copies
// is one the dashboard actually runs.
func TestBuiltinActivityLensIsTheSpecExample(t *testing.T) {
	embedded, err := builtinLensFiles.ReadFile("lenses/activity.lens.html")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile(filepath.Join(lensSpecDir(t), "examples", "activity.lens.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, example) {
		t.Fatal("lenses/activity.lens.html drifted from spec/lenses/examples/activity.lens.html; copy the example over")
	}
}

func TestBuiltinLensesLoad(t *testing.T) {
	registry, err := loadBuiltinLenses(builtinLensFiles)
	if err != nil {
		t.Fatal(err)
	}
	activity, ok := registry.byID["activity"]
	if !ok || activity.info.Source != LensSourceBuiltin || activity.info.Title != "Activity" {
		t.Fatalf("activity lens = %#v, %v", activity.info, ok)
	}
}

func TestLoadBuiltinLensesRejectsABrokenLens(t *testing.T) {
	manifest := func(id, api string) string {
		return `<!doctype html><script type="application/beacon-lens+json">{"id":"` + id + `","title":"T","version":1,"api":"` + api + `"}</script>`
	}
	cases := map[string]fstest.MapFS{
		"no manifest":        {"lenses/a.lens.html": {Data: []byte("<!doctype html><p>hi")}},
		"invalid manifest":   {"lenses/a.lens.html": {Data: []byte(manifest("a", "beacon.lens.v9"))}},
		"file name mismatch": {"lenses/a.lens.html": {Data: []byte(manifest("b", "beacon.lens.v1"))}},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadBuiltinLenses(files); err == nil {
				t.Fatal("loadBuiltinLenses accepted a broken built-in")
			}
		})
	}
}

func TestLensListEndpoint(t *testing.T) {
	handler := lensTestHandler(t)
	rec := serve(handler, http.MethodGet, "/api/lenses")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp LensListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range resp.Lenses {
		if l.ID == "activity" && l.Source == LensSourceBuiltin && l.Version >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("lenses = %#v, want the activity built-in", resp.Lenses)
	}
	if rec := serve(handler, http.MethodPost, "/api/lenses"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

func TestLensFrameIsServedSandboxedWithThePrelude(t *testing.T) {
	handler := lensTestHandler(t)
	rec := serve(handler, http.MethodGet, "/lenses/frame/activity")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Content-Security-Policy"); got != lensFrameCSP {
		t.Fatalf("CSP = %q", got)
	}
	for _, directive := range []string{"default-src 'none'", "connect-src 'none'", "frame-ancestors 'self'", "sandbox allow-scripts"} {
		if !strings.Contains(lensFrameCSP, directive) {
			t.Errorf("frame CSP lacks %q", directive)
		}
	}
	if strings.Contains(lensFrameCSP, "allow-same-origin") || strings.Contains(lensFrameCSP, "unsafe-eval") {
		t.Error("frame CSP must not grant allow-same-origin or unsafe-eval")
	}
	if got := h.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q; the dashboard must be able to frame a lens", got)
	}
	if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", h)
	}

	body := rec.Body.String()
	if !strings.HasPrefix(body, "<!doctype html><style>") {
		t.Fatalf("frame does not start with the doctype then the prelude: %.80q", body)
	}
	prelude := strings.Index(body, "Object.defineProperty(window, \"beacon\"")
	firstLensScript := strings.Index(body, "window.beacon.getTrace()")
	if prelude < 0 || firstLensScript < 0 || prelude > firstLensScript {
		t.Fatal("window.beacon must be defined before the lens's own script")
	}
	if !strings.Contains(body, `<script type="application/beacon-lens+json">`) {
		t.Fatal("frame lost the lens's manifest")
	}

	head := serve(handler, http.MethodHead, "/lenses/frame/activity")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d with %d bytes", head.Code, head.Body.Len())
	}
	for _, url := range []string{"/lenses/frame/nope", "/lenses/frame/", "/lenses/frame/activity/extra", "/lenses/frame/ACTIVITY"} {
		if rec := serve(handler, http.MethodGet, url); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", url, rec.Code)
		}
	}
	if rec := serve(handler, http.MethodPost, "/lenses/frame/activity"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

// The frame route loosens framing for itself only. Every other page keeps refusing to be framed.
func TestDashboardPagesStillRefuseFraming(t *testing.T) {
	handler := lensTestHandler(t)
	for _, url := range []string{"/", "/session.html", "/api/lenses"} {
		rec := serve(handler, http.MethodGet, url)
		if rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("%s headers = %v, want framing refused", url, rec.Header())
		}
	}
}

func TestWithLensPrelude(t *testing.T) {
	prelude := string(lensPrelude())
	cases := map[string]struct{ in, want string }{
		"doctype":            {"<!doctype html><p>x", "<!doctype html>" + prelude + "<p>x"},
		"doctype upper case": {"<!DOCTYPE html>\n<p>x", "<!DOCTYPE html>" + prelude + "\n<p>x"},
		"leading whitespace": {"\n  <!doctype html><p>x", "<!doctype html>" + prelude + "<p>x"},
		"byte order mark":    {"\xef\xbb\xbf<!doctype html><p>x", "<!doctype html>" + prelude + "<p>x"},
		"no doctype":         {"<html><p>x", prelude + "<html><p>x"},
		"doctype not first":  {"<p>x<!doctype html>", prelude + "<p>x<!doctype html>"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := string(withLensPrelude([]byte(tc.in))); got != tc.want {
				t.Fatalf("withLensPrelude(%q) = %q", tc.in, got)
			}
		})
	}
}

// The prelude is inlined into <script> and <style> elements, so it must never contain the text that
// would close them early.
func TestLensPreludeCannotCloseItsOwnElements(t *testing.T) {
	if strings.Contains(strings.ToLower(lensPreludeJS), "</script") {
		t.Fatal("prelude.js contains </script")
	}
	if strings.Contains(strings.ToLower(lensTokensCSS), "</style") {
		t.Fatal("tokens.css contains </style")
	}
}

// The tokens the spec documents are the tokens the dashboard defines, with the same values.
func TestLensTokensMatchTheSpec(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join(lensSpecDir(t), "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("(?m)^\\s*\\| `(--beacon-[a-z-]+)` \\| (.+?) \\|$")
	css := regexp.MustCompile(`(?m)^\s*(--beacon-[a-z-]+):\s*(.+?);$`)
	defined := map[string]string{}
	for _, m := range css.FindAllStringSubmatch(lensTokensCSS, -1) {
		defined[m[1]] = m[2]
	}
	documented := row.FindAllStringSubmatch(string(spec), -1)
	if len(documented) == 0 {
		t.Fatal("no token rows found in SPEC.md")
	}
	if len(documented) != len(defined) {
		t.Errorf("SPEC.md documents %d tokens, tokens.css defines %d", len(documented), len(defined))
	}
	for _, m := range documented {
		name, value := m[1], m[2]
		got, ok := defined[name]
		if !ok {
			t.Errorf("%s is documented but not defined", name)
			continue
		}
		// Literal values are written in backticks; prose descriptions (the font stack) are not.
		if strings.HasPrefix(value, "`") && strings.Trim(value, "`") != got {
			t.Errorf("%s: spec %s, tokens.css %q", name, value, got)
		}
	}
}
