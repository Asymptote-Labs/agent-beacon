package dashboard

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Built-in lenses ship inside the binary. Each is a complete lens file, held to the same spec as a
// user's: the registry parses and validates its manifest at startup rather than trusting it.
//
//go:embed lenses/*.lens.html
var builtinLensFiles embed.FS

//go:embed lenses/prelude.js
var lensPreludeJS string

//go:embed lenses/tokens.css
var lensTokensCSS string

// lensFrameCSP is the policy spec/lenses/SPEC.md promises. The sandbox directive repeats the
// iframe's sandbox attribute, so a lens URL opened on its own -- outside the dashboard's frame --
// still runs in an opaque origin with no network.
const lensFrameCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; " +
	"img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; " +
	"form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts"

// lensFramePrefix is where the frame for a lens is served: /lenses/frame/<id>.
const lensFramePrefix = "/lenses/frame/"

// LensSourceBuiltin marks a lens that ships in the binary.
const LensSourceBuiltin = "builtin"

// LensInfo is one lens as the dashboard lists it.
type LensInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Version     int    `json:"version"`
	Source      string `json:"source"`
}

// LensListResponse is the /api/lenses payload.
type LensListResponse struct {
	Lenses []LensInfo `json:"lenses"`
}

type lens struct {
	info LensInfo
	html []byte
}

type lensRegistry struct {
	byID  map[string]lens
	order []string
}

// loadBuiltinLenses reads every built-in lens. A built-in that breaks the spec is a build defect,
// so it fails dashboard startup instead of being skipped.
func loadBuiltinLenses(files fs.FS) (*lensRegistry, error) {
	names, err := fs.Glob(files, "lenses/*.lens.html")
	if err != nil {
		return nil, err
	}
	registry := &lensRegistry{byID: map[string]lens{}}
	for _, name := range names {
		html, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		if len(html) > asymptoteobserve.LensMaxBytes {
			return nil, fmt.Errorf("built-in lens %s is %d bytes, over the %d-byte limit", name, len(html), asymptoteobserve.LensMaxBytes)
		}
		manifest, err := asymptoteobserve.ParseLensManifest(html)
		if err != nil {
			return nil, fmt.Errorf("built-in lens %s: %w", name, err)
		}
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("built-in lens %s: %w", name, err)
		}
		if want := manifest.ID + ".lens.html"; path.Base(name) != want {
			return nil, fmt.Errorf("built-in lens %s declares id %q; name the file %s", name, manifest.ID, want)
		}
		if _, dup := registry.byID[manifest.ID]; dup {
			return nil, fmt.Errorf("two built-in lenses declare id %q", manifest.ID)
		}
		registry.byID[manifest.ID] = lens{
			info: LensInfo{
				ID:          manifest.ID,
				Title:       manifest.Title,
				Description: manifest.Description,
				Icon:        manifest.Icon,
				Version:     manifest.Version,
				Source:      LensSourceBuiltin,
			},
			html: html,
		}
		registry.order = append(registry.order, manifest.ID)
	}
	sort.Strings(registry.order)
	return registry, nil
}

func (r *lensRegistry) list() LensListResponse {
	out := LensListResponse{Lenses: make([]LensInfo, 0, len(r.order))}
	for _, id := range r.order {
		out.Lenses = append(out.Lenses, r.byID[id].info)
	}
	return out
}

// lensPrelude is what the dashboard puts ahead of every lens: the style tokens, then the script
// that defines window.beacon. Both are inline because the frame's CSP allows nothing else.
func lensPrelude() []byte {
	return []byte("<style>" + lensTokensCSS + "</style><script>" + lensPreludeJS + "</script>")
}

// withLensPrelude inserts the prelude right after the lens's doctype, so the document keeps its
// standards mode and the prelude still runs before any lens script. A prelude ahead of <html> is
// parsed into <head>, which is where it belongs.
func withLensPrelude(html []byte) []byte {
	prelude := lensPrelude()
	rest := bytes.TrimPrefix(html, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimLeft(rest, " \t\r\n")
	if len(trimmed) >= len("<!doctype") && strings.EqualFold(string(trimmed[:len("<!doctype")]), "<!doctype") {
		if end := bytes.IndexByte(trimmed, '>'); end >= 0 {
			head := trimmed[:end+1]
			out := make([]byte, 0, len(html)+len(prelude))
			out = append(out, head...)
			out = append(out, prelude...)
			return append(out, trimmed[end+1:]...)
		}
	}
	out := make([]byte, 0, len(html)+len(prelude))
	out = append(out, prelude...)
	return append(out, rest...)
}

// serveLensFrame serves one lens as the document of a sandboxed frame. It replaces the dashboard's
// own security headers: the dashboard refuses to be framed at all, and a lens must be framed by
// the dashboard and by nothing else.
func (r *lensRegistry) serveLensFrame(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(req.URL.Path, lensFramePrefix)
	found, ok := r.byID[id]
	if !ok {
		http.NotFound(w, req)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", lensFrameCSP)
	h.Del("X-Frame-Options")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(withLensPrelude(found.html))
}
