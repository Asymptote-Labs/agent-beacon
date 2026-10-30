package asymptoteobserve

import "testing"

func TestFilePathFromURI(t *testing.T) {
	for uri, want := range map[string]string{
		"file:///tmp/report.md":                  "/tmp/report.md",
		"file://localhost/tmp/report.md":         "/tmp/report.md",
		"file:///C:/Users/dev/.aws/credentials":  "C:/Users/dev/.aws/credentials",
		"file:///c%3A/Users/dev/My%20Notes.md":   "c:/Users/dev/My Notes.md",
		"file://server/share/report.md":          "//server/share/report.md",
		"https://example.com/C:/not-a-file-path": "",
		"/tmp/plain/path":                        "",
	} {
		if got := FilePathFromURI(uri); got != want {
			t.Errorf("FilePathFromURI(%q) = %q, want %q", uri, got, want)
		}
	}
}
