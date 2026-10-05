// Package lensstore manages the local lens store: the lens files a user installed with
// `beacon lenses add`, which the dashboard offers next to its built-in lenses.
//
// The store is a flat directory of <id>.lens.html files under the endpoint base directory. The
// dashboard only reads it; every write goes through this package from the CLI, so the dashboard
// stays read-only. A file is checked against spec/lenses/SPEC.md when it is installed and again
// whenever it is read, because a person can edit the store by hand.
package lensstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// FileSuffix is the extension every stored lens carries.
const FileSuffix = ".lens.html"

// Lens is one installed lens.
type Lens struct {
	Manifest asymptoteobserve.LensManifestV1
	Path     string
}

// Problem is a file in the store that is not a valid lens. Listing reports it instead of failing,
// so one broken file does not hide every other lens.
type Problem struct {
	Path string
	Err  error
}

// Dir returns the store directory for the user or system endpoint.
func Dir(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), "lenses")
}

// PathFor returns where the lens with id is stored. It refuses an ID that is not well formed, so a
// caller can never be steered outside the store.
func PathFor(dir, id string) (string, error) {
	if !asymptoteobserve.ValidLensID(id) {
		return "", fmt.Errorf("%q is not a lens id", id)
	}
	return filepath.Join(dir, id+FileSuffix), nil
}

// List returns the valid lenses in dir, sorted by ID, and the files that are not valid lenses. A
// missing directory is an empty store.
func List(dir string) ([]Lens, []Problem, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var lenses []Lens
	var problems []Problem
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileSuffix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		_, manifest, err := readFile(path, strings.TrimSuffix(entry.Name(), FileSuffix))
		if err != nil {
			problems = append(problems, Problem{Path: path, Err: err})
			continue
		}
		lenses = append(lenses, Lens{Manifest: manifest, Path: path})
	}
	sort.Slice(lenses, func(i, j int) bool { return lenses[i].Manifest.ID < lenses[j].Manifest.ID })
	return lenses, problems, nil
}

// Read returns the stored lens with id, checked again as it is read.
func Read(dir, id string) ([]byte, asymptoteobserve.LensManifestV1, error) {
	path, err := PathFor(dir, id)
	if err != nil {
		return nil, asymptoteobserve.LensManifestV1{}, err
	}
	return readFile(path, id)
}

// readFile checks a stored file and that it declares the ID its name promises.
func readFile(path, id string) ([]byte, asymptoteobserve.LensManifestV1, error) {
	html, err := os.ReadFile(path)
	if err != nil {
		return nil, asymptoteobserve.LensManifestV1{}, err
	}
	manifest, err := asymptoteobserve.CheckLensFile(html)
	if err != nil {
		return nil, manifest, err
	}
	if manifest.ID != id {
		return nil, manifest, fmt.Errorf("file is named for %q but declares id %q", id, manifest.ID)
	}
	return html, manifest, nil
}

// InstallOptions controls Install.
type InstallOptions struct {
	// Reserved are IDs the store may not use: the dashboard's built-in lenses.
	Reserved map[string]bool
	// Force replaces an installed lens even when the new file's version is not newer.
	Force bool
}

// Install checks the lens file at src and copies it into dir under its manifest ID. It returns
// the installed lens and the version it replaced: 0 for a new lens, -1 when it replaced a file that
// was not a valid lens. Replacing an installed lens
// needs a newer version or Force, so reinstalling an old copy by mistake does not downgrade it.
func Install(dir, src string, opts InstallOptions) (Lens, int, error) {
	html, err := os.ReadFile(src)
	if err != nil {
		return Lens{}, 0, err
	}
	manifest, err := asymptoteobserve.CheckLensFile(html)
	if err != nil {
		return Lens{}, 0, fmt.Errorf("%s is not a valid lens: %w", src, err)
	}
	if opts.Reserved[manifest.ID] {
		return Lens{}, 0, fmt.Errorf("%q is a built-in lens id; give this lens another id", manifest.ID)
	}
	dest, err := PathFor(dir, manifest.ID)
	if err != nil {
		return Lens{}, 0, err
	}
	replaced := 0
	if _, current, err := readFile(dest, manifest.ID); err == nil {
		replaced = current.Version
		if !opts.Force && manifest.Version <= current.Version {
			return Lens{}, 0, fmt.Errorf("lens %q version %d is already installed; bump the version or pass --force", manifest.ID, current.Version)
		}
	} else if _, statErr := os.Stat(dest); statErr == nil {
		// An unreadable or invalid file is in the way. Replacing it is what the user asked for.
		replaced = -1
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Lens{}, 0, err
	}
	tmp, err := os.CreateTemp(dir, ".install-*"+FileSuffix+".tmp")
	if err != nil {
		return Lens{}, 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(html); err != nil {
		tmp.Close()
		return Lens{}, 0, err
	}
	if err := tmp.Close(); err != nil {
		return Lens{}, 0, err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return Lens{}, 0, err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return Lens{}, 0, err
	}
	return Lens{Manifest: manifest, Path: dest}, replaced, nil
}

// Remove deletes the stored lens with id and returns its path.
func Remove(dir, id string) (string, error) {
	path, err := PathFor(dir, id)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no installed lens %q in %s", id, dir)
		}
		return "", err
	}
	return path, nil
}
