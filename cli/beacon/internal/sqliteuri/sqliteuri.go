// Package sqliteuri builds the file: URIs Beacon hands modernc.org/sqlite.
package sqliteuri

import (
	"net/url"
	"path/filepath"
	"strings"
)

// File renders an OS path as a file: URI carrying query as its parameters.
//
// The URI form is not optional: modernc.org/sqlite forwards query parameters such as mode=ro and
// _pragma to SQLite only when the DSN starts with "file:". The path is made absolute,
// slash-separated and percent-escaped, so a '#', '?' or '%' in a directory name stays part of the
// filename instead of starting a fragment, a query or an escape that opens a different file.
func File(path string, query url.Values) string {
	abs := path
	if resolved, err := filepath.Abs(path); err == nil {
		abs = resolved
	}
	u := fileURL(filepath.ToSlash(abs))
	u.RawQuery = query.Encode()
	return u.String()
}

// fileURL splits an already slash-separated path into the host and path of a file: URL.
//
// Handing url.URL a raw Windows path does not produce a usable URI: C:/Users/me/x.db has no leading
// slash, so SQLite would read "C:" as the URI's authority. A UNC path (\\server\share\x.db, slashed
// to //server/share/x.db) names a host, and is the one case where a file: URI has one. Everything
// else is a plain local path and gets the leading slash that keeps a drive letter inside the path.
func fileURL(slashed string) url.URL {
	if rest, ok := strings.CutPrefix(slashed, "//"); ok {
		host, remainder, _ := strings.Cut(rest, "/")
		if host != "" {
			return url.URL{Scheme: "file", Host: host, Path: "/" + remainder}
		}
	}
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return url.URL{Scheme: "file", Path: slashed}
}
