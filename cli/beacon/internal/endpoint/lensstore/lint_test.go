package lensstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lintManifest = `<script type="application/beacon-lens+json">{"id":"x","title":"X","version":1,"api":"beacon.lens.v1"}</script>`

func TestLintFlagsWhatTheSandboxBlocks(t *testing.T) {
	cases := map[string]string{
		`el.innerHTML = data.trace.title`:                    "textContent",
		`el.insertAdjacentHTML("beforeend", s)`:              "textContent",
		`eval("1 + 1")`:                                      "evaluates a string",
		`fetch("/api/status")`:                               "network request",
		`new WebSocket("ws://x")`:                            "network request",
		`<script src="https://cdn.example/lib.js"></script>`: "by URL",
		`<img src="//cdn.example/a.png">`:                    "by URL",
		`body { background: url(https://x/y.png) }`:          "stylesheet resource",
		`@import "theme.css";`:                               "stylesheet resource",
		`localStorage.setItem("k", "v")`:                     "storage",
		`window.parent.postMessage({}, "*")`:                 "navigates a window",
		`location.href = "/elsewhere"`:                       "navigates a window",
		`main { height: 100vh }`:                             "viewport",
		`new Worker(url)`:                                    "worker",
	}
	for line, want := range cases {
		html := "<!doctype html>\n" + lintManifest + "\n<script>window.beacon.getTrace()</script>\n" + line + "\n"
		findings := Lint([]byte(html))
		var hit *LintFinding
		for i := range findings {
			if strings.Contains(findings[i].Message, want) {
				hit = &findings[i]
			}
		}
		if hit == nil || hit.Line != 4 || hit.Severity != LintWarning {
			t.Errorf("Lint(%q) = %+v, want a line-4 warning about %q", line, findings, want)
		}
		if HasLintErrors(findings) {
			t.Errorf("Lint(%q) reported an error for a warning-level problem: %+v", line, findings)
		}
	}
}

// Only height itself sizes an element to its container; the hyphenated properties do not.
func TestLintViewportHeightIgnoresOtherHeightProperties(t *testing.T) {
	for _, css := range []string{"p { line-height: 100% }", "img { max-height: 100% }", "td { min-height: 100% }"} {
		html := "<!doctype html>\n" + lintManifest + "\n<script>window.beacon.getTrace()</script>\n<style>" + css + "</style>\n"
		if findings := Lint([]byte(html)); len(findings) != 0 {
			t.Errorf("Lint(%q) = %+v, want no findings", css, findings)
		}
	}
	for _, css := range []string{"html { height: 100% }", "body{height:100%}"} {
		html := "<!doctype html>\n" + lintManifest + "\n<script>window.beacon.getTrace()</script>\n<style>" + css + "</style>\n"
		if findings := Lint([]byte(html)); len(findings) != 1 {
			t.Errorf("Lint(%q) = %+v, want the viewport warning", css, findings)
		}
	}
}

func TestLintClean(t *testing.T) {
	html := "<!doctype html>\r\n" + lintManifest + "\r\n<p id=out></p>\r\n<script>\r\nwindow.beacon.getTrace().then(function (d) { out.textContent = d.trace.id; });\r\n</script>\r\n"
	if findings := Lint([]byte(html)); len(findings) != 0 {
		t.Fatalf("a clean lens (with CRLF line endings) got %+v", findings)
	}
}

func TestLintErrorsAndMissingGetTrace(t *testing.T) {
	findings := Lint([]byte("<p>no manifest</p>"))
	if !HasLintErrors(findings) {
		t.Fatalf("a lens without a manifest has no error: %+v", findings)
	}
	found := false
	for _, f := range findings {
		if strings.Contains(f.Message, "never calls window.beacon.getTrace()") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing getTrace not reported: %+v", findings)
	}
}

// The hostile e2e probe does everything the spec forbids; lint should say so.
func TestLintCatchesTheHostileProbe(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "dashboard", "testdata", "lens-e2e", "fixtures", "probe.lens.html"))
	if err != nil {
		t.Fatal(err)
	}
	findings := Lint(html)
	if HasLintErrors(findings) || len(findings) < 6 {
		t.Fatalf("probe lint = %+v, want several warnings and no errors", findings)
	}
}
