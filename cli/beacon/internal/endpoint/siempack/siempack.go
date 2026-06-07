package siempack

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const logPathToken = "{{LOG_PATH}}"

type File struct {
	Name            string
	Content         string
	TemplateLogPath bool
	JSONEscape      bool
}

func ReadFile(fsys fs.FS, path string) (string, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func RenderLogPath(content, logPath string) string {
	return strings.ReplaceAll(content, logPathToken, logPath)
}

// JSONEscapeForString escapes s for safe embedding inside a JSON string literal
// (the surrounding quotes are not included).
func JSONEscapeForString(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func Install(outputDir string, files []File, logPath string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	for _, file := range files {
		content := file.Content
		if file.TemplateLogPath {
			lp := logPath
			if file.JSONEscape {
				lp = JSONEscapeForString(lp)
			}
			content = RenderLogPath(content, lp)
		}
		if err := os.WriteFile(filepath.Join(outputDir, file.Name), []byte(content), Mode(file.Name)); err != nil {
			return err
		}
	}
	return nil
}

func Mode(name string) os.FileMode {
	if strings.HasSuffix(name, ".sh") {
		return 0755
	}
	return 0644
}

// Asset describes one embedded pack file and how it maps into an installed pack.
type Asset struct {
	// Source is the path within the embedded FS, e.g. "pack/vector.toml.tmpl".
	Source string
	// Name is the output filename, e.g. "vector.toml".
	Name string
	// TemplateLogPath substitutes the runtime log path into the content on install.
	TemplateLogPath bool
	// JSONEscape escapes the substituted log path for embedding in a JSON string.
	JSONEscape bool
}

// Pack is an embedded content pack for a single forwarding destination. It
// centralizes the asset-read, file-list, render, and install boilerplate that
// would otherwise be duplicated across destination packages.
type Pack struct {
	// Label prefixes asset-read error messages, e.g. "sumo".
	Label            string
	FS               fs.FS
	DefaultLogPath   string
	DefaultOutputDir string
	Assets           []Asset
}

// WithFS returns a copy of the pack reading from fsys instead of p.FS. It lets
// callers inject a test FS to exercise read-error paths.
func (p Pack) WithFS(fsys fs.FS) Pack {
	p.FS = fsys
	return p
}

// Read returns the embedded asset at path, wrapping read errors with the label.
func (p Pack) Read(path string) (string, error) {
	content, err := ReadFile(p.FS, path)
	if err != nil {
		return "", fmt.Errorf("%s pack asset %q: %w", p.Label, path, err)
	}
	return content, nil
}

// MustRead returns the embedded asset at path or panics. Intended for test and
// package-init use where the embedded FS is always present.
func (p Pack) MustRead(path string) string {
	content, err := p.Read(path)
	if err != nil {
		panic(err.Error())
	}
	return content
}

// Files reads every asset and returns the pack's files. Log-path substitution is
// deferred to Install via the per-file TemplateLogPath flag.
func (p Pack) Files() ([]File, error) {
	files := make([]File, 0, len(p.Assets))
	for _, a := range p.Assets {
		content, err := p.Read(a.Source)
		if err != nil {
			return nil, err
		}
		files = append(files, File{
			Name:            a.Name,
			Content:         content,
			TemplateLogPath: a.TemplateLogPath,
			JSONEscape:      a.JSONEscape,
		})
	}
	return files, nil
}

// Render reads a single template asset and substitutes logPath (defaulting to
// DefaultLogPath). Used by print-config snippet helpers.
func (p Pack) Render(assetPath, logPath string) (string, error) {
	if logPath == "" {
		logPath = p.DefaultLogPath
	}
	content, err := p.Read(assetPath)
	if err != nil {
		return "", err
	}
	return RenderLogPath(content, logPath), nil
}

// Install writes the pack files to outputDir (defaulting to DefaultOutputDir),
// substituting logPath (defaulting to DefaultLogPath) into templated files.
func (p Pack) Install(outputDir, logPath string) error {
	if outputDir == "" {
		outputDir = p.DefaultOutputDir
	}
	if logPath == "" {
		logPath = p.DefaultLogPath
	}
	files, err := p.Files()
	if err != nil {
		return err
	}
	return Install(outputDir, files, logPath)
}
