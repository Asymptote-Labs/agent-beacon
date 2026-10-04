package dashboard

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lensstore"
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

// Lens sources.
const (
	// LensSourceBuiltin marks a lens that ships in the binary.
	LensSourceBuiltin = "builtin"
	// LensSourceFile marks a lens read from a file named in Options.LensFiles.
	LensSourceFile = "file"
	// LensSourceStore marks a lens installed with `beacon lenses add`.
	LensSourceStore = "store"
)

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
	html []byte // a built-in's document
	path string // a file lens's location, read on every request
}

type lensRegistry struct {
	byID  map[string]lens
	order []string
	// storeDir is the lens store, read on every request so `beacon lenses add` and `remove` take
	// effect without restarting the dashboard. A store lens never shadows a built-in or file lens.
	storeDir string
}

// BuiltinLenses returns the lenses that ship in the binary, sorted by ID.
func BuiltinLenses() ([]LensInfo, error) {
	registry, err := loadBuiltinLenses(builtinLensFiles)
	if err != nil {
		return nil, err
	}
	return registry.list().Lenses, nil
}

// BuiltinLensIDs returns the IDs the built-in lenses hold, which the lens store may not use.
func BuiltinLensIDs() (map[string]bool, error) {
	builtins, err := BuiltinLenses()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(builtins))
	for _, l := range builtins {
		ids[l.ID] = true
	}
	return ids, nil
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
		manifest, err := asymptoteobserve.CheckLensFile(html)
		if err != nil {
			return nil, fmt.Errorf("built-in lens %s: %w", name, err)
		}
		if want := manifest.ID + ".lens.html"; path.Base(name) != want {
			return nil, fmt.Errorf("built-in lens %s declares id %q; name the file %s", name, manifest.ID, want)
		}
		if err := registry.add(lens{info: lensInfo(manifest, LensSourceBuiltin), html: html}); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// addLensFiles registers lenses read from disk, such as a lens under development. Each file is
// checked now and again on every request, so an edit shows on the next reload and a file that
// stops being a valid lens stops being served. A file lens cannot take a built-in's ID.
func (r *lensRegistry) addLensFiles(paths []string) error {
	for _, file := range paths {
		html, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		manifest, err := asymptoteobserve.CheckLensFile(html)
		if err != nil {
			return fmt.Errorf("lens %s: %w", file, err)
		}
		if err := r.add(lens{info: lensInfo(manifest, LensSourceFile), path: file}); err != nil {
			return fmt.Errorf("lens %s: %w", file, err)
		}
	}
	return nil
}

func (r *lensRegistry) add(l lens) error {
	if _, dup := r.byID[l.info.ID]; dup {
		return fmt.Errorf("lens id %q is already taken", l.info.ID)
	}
	r.byID[l.info.ID] = l
	r.order = append(r.order, l.info.ID)
	sort.Strings(r.order)
	return nil
}

func lensInfo(manifest asymptoteobserve.LensManifestV1, source string) LensInfo {
	return LensInfo{
		ID:          manifest.ID,
		Title:       manifest.Title,
		Description: manifest.Description,
		Icon:        manifest.Icon,
		Version:     manifest.Version,
		Source:      source,
	}
}

// document returns the lens document to serve. A file lens is re-read and re-checked, and must
// still declare the ID it was registered under.
func (l lens) document() ([]byte, error) {
	if l.path == "" {
		return l.html, nil
	}
	html, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err
	}
	manifest, err := asymptoteobserve.CheckLensFile(html)
	if err != nil {
		return nil, err
	}
	if manifest.ID != l.info.ID {
		return nil, fmt.Errorf("lens now declares id %q, not %q", manifest.ID, l.info.ID)
	}
	return html, nil
}

func (r *lensRegistry) list() LensListResponse {
	out := LensListResponse{Lenses: make([]LensInfo, 0, len(r.order))}
	for _, id := range r.order {
		out.Lenses = append(out.Lenses, r.byID[id].info)
	}
	if r.storeDir != "" {
		// A broken store file is left out here; `beacon lenses list` names it.
		stored, _, _ := lensstore.List(r.storeDir)
		for _, l := range stored {
			if _, taken := r.byID[l.Manifest.ID]; !taken {
				out.Lenses = append(out.Lenses, lensInfo(l.Manifest, LensSourceStore))
			}
		}
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
	var (
		document []byte
		err      error
	)
	if found, ok := r.byID[id]; ok {
		document, err = found.document()
	} else if r.storeDir != "" && asymptoteobserve.ValidLensID(id) {
		document, _, err = lensstore.Read(r.storeDir, id)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, req)
			return
		}
	} else {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("lens %s: %w", id, err))
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
	_, _ = w.Write(withLensPrelude(document))
}
