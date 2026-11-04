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
const inventoryLogPathToken = "{{INVENTORY_LOG_PATH}}"
const backfillLogPathToken = "{{BACKFILL_LOG_PATH}}"

// BackfillLogName is the file every Beacon Cloud connect stages the bounded session backfill in,
// beside the runtime log. Its name does not start with the runtime log's, so readers of the runtime
// log and its archives never see it.
const BackfillLogName = "cloud-backfill.jsonl"

type File struct {
	Name            string
	Content         string
	TemplateLogPath bool
}

func ReadFile(fsys fs.FS, path string) (string, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RenderLogPath substitutes the log path tokens in content, escaped for the format of the file
// name (a trailing .tmpl is ignored). Sibling paths are derived before escaping, so escapes never
// reach filepath.Dir.
func RenderLogPath(name, content, logPath string) string {
	escape := logPathEscaper(name)
	return strings.NewReplacer(
		logPathToken, escape(logPath),
		inventoryLogPathToken, escape(InventoryLogPath(logPath)),
		backfillLogPathToken, escape(BackfillLogPath(logPath)),
	).Replace(content)
}

// logPathEscaper returns how a path is written where the templates of that format place their
// tokens: TOML basic strings, YAML double-quoted scalars, JSON strings, and the default of a
// double-quoted "${VAR:-...}" shell expansion. Any other file gets the path verbatim.
func logPathEscaper(name string) func(string) string {
	switch filepath.Ext(strings.TrimSuffix(name, ".tmpl")) {
	case ".toml":
		return tomlEscape
	case ".yaml", ".yml":
		return yamlEscape
	case ".json":
		return JSONEscapeForString
	case ".sh":
		return shellDefaultEscaper.Replace
	case ".xml":
		// Wazuh's os_xml keeps element text verbatim and decodes no entities, so escaping would change the path.
		return verbatim
	}
	return verbatim
}

func verbatim(value string) string { return value }

// tomlEscape takes JSON string escapes, which TOML basic strings share, plus DEL, which TOML forbids.
func tomlEscape(value string) string {
	return strings.ReplaceAll(JSONEscapeForString(value), "\x7f", `\u007f`)
}

// yamlEscape takes JSON string escapes, which YAML double-quoted scalars share, plus the characters
// YAML forbids or folds: DEL, the C1 controls (NEL reads as a line break), the BOM, U+FFFE and U+FFFF.
func yamlEscape(value string) string {
	escaped := JSONEscapeForString(value)
	if strings.IndexFunc(escaped, yamlNeedsEscape) < 0 {
		return escaped
	}
	var b strings.Builder
	for _, r := range escaped {
		if yamlNeedsEscape(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func yamlNeedsEscape(r rune) bool {
	return (r >= 0x7f && r <= 0x9f) || r == 0xfeff || r == 0xfffe || r == 0xffff
}

// shellDefaultEscaper writes a path inside "${VAR:-...}": a backslash before \ $ ` and ", which keeps
// them literal, and } quoted rather than backslashed, because bash 3.2 (macOS /bin/sh) keeps the
// backslash of \} there.
var shellDefaultEscaper = strings.NewReplacer(`\`, `\\`, `$`, `\$`, "`", "\\`", `"`, `\"`, `}`, `"}"`)

// BackfillLogPath is BackfillLogName beside logPath.
func BackfillLogPath(logPath string) string {
	if logPath == "" {
		return BackfillLogName
	}
	return filepath.Join(filepath.Dir(logPath), BackfillLogName)
}

func InventoryLogPath(logPath string) string {
	if logPath == "" {
		return "inventory_state.jsonl"
	}
	return filepath.Join(filepath.Dir(logPath), "inventory_state.jsonl")
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
			content = RenderLogPath(file.Name, content, logPath)
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
	return RenderLogPath(assetPath, content, logPath), nil
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
