package threatrules

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A rule pack is a directory of rules and the definitions beside them (see Library): files whose
// names say what they are, anywhere under it. Every reader of a pack (a directory linted or
// scanned, the rule store, the embedded baseline, a pack being installed or pulled) tells its
// files by PackFileKind, and the loaders find them with ReadPackFS, so that what counts as a pack
// file is decided once.

// FileKind is the kind of a pack file, told by its name.
type FileKind int

const (
	RuleFile FileKind = iota + 1
	CatalogFile
	PredicatesFile
)

// The suffixes of the pack files' names, one for each kind.
const (
	RuleFileSuffix       = ".rule.yaml"
	CatalogFileSuffix    = ".catalog.yaml"
	PredicatesFileSuffix = ".predicates.yaml"
)

// Suffix is the suffix of the names of files of the kind.
func (k FileKind) Suffix() string {
	switch k {
	case RuleFile:
		return RuleFileSuffix
	case CatalogFile:
		return CatalogFileSuffix
	case PredicatesFile:
		return PredicatesFileSuffix
	}
	return ""
}

// Definitions reports whether a file of the kind is one of a pack's definitions.
func (k FileKind) Definitions() bool { return k == CatalogFile || k == PredicatesFile }

// kindOf tells a file's kind by the suffix of its name alone.
func kindOf(name string) (FileKind, bool) {
	for _, k := range []FileKind{RuleFile, CatalogFile, PredicatesFile} {
		if strings.HasSuffix(name, k.Suffix()) {
			return k, true
		}
	}
	return 0, false
}

// PackFileKind returns the kind of a pack file by its name, a path within the pack with / or \
// separators; ok is false for any other file. It is false also for the AppleDouble files macOS
// writes beside a file it copies (._name, and anything under __MACOSX), which carry a pack file's
// name and none of its content.
func PackFileKind(name string) (kind FileKind, ok bool) {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "__MACOSX" || strings.HasPrefix(part, "._") {
			return 0, false
		}
	}
	return kindOf(name)
}

// Head is what a pack file says about itself, read leniently before it is decoded: the name it
// declares (a rule's id, a catalog's or a library's name) and the spec it needs.
type Head struct {
	Kind       FileKind
	Name, Spec string
}

// ReadHead reads the head of a pack file of the kind its name says. An error says the file is no
// YAML a head can be read from; the strict decode that follows reports it in full.
func ReadHead(name string, data []byte) (Head, error) {
	kind, ok := kindOf(name)
	if !ok {
		return Head{}, fmt.Errorf("%s: not a pack file (want %s, %s or %s)", name, RuleFileSuffix, CatalogFileSuffix, PredicatesFileSuffix)
	}
	return readHead(kind, data)
}

func readHead(kind FileKind, data []byte) (Head, error) {
	var head struct {
		ID      string `yaml:"id"`
		Catalog string `yaml:"catalog"`
		Library string `yaml:"library"`
		Spec    string `yaml:"spec"`
	}
	err := yaml.Unmarshal(data, &head)
	h := Head{Kind: kind, Spec: head.Spec}
	switch kind {
	case RuleFile:
		h.Name = head.ID
	case CatalogFile:
		h.Name = head.Catalog
	case PredicatesFile:
		h.Name = head.Library
	}
	return h, err
}

// Unsupported returns, for a file that declares a spec newer than this engine supports, the error
// a loader sets it aside with; nil otherwise. A malformed spec is the decoder's to report.
func (h Head) Unsupported() *UnsupportedSpecError {
	v, err := parseSpec(h.Spec)
	if err != nil || !v.newerThan(supportedSpec) {
		return nil
	}
	if h.Kind == RuleFile {
		return &UnsupportedSpecError{RuleID: h.Name, Required: h.Spec}
	}
	return &UnsupportedSpecError{Definitions: h.String(), Required: h.Spec}
}

// String names what the file declares: rule x, catalog x, library x.
func (h Head) String() string {
	what := map[FileKind]string{RuleFile: "rule", CatalogFile: "catalog", PredicatesFile: "library"}[h.Kind]
	return what + " " + cmp.Or(h.Name, "(no name)")
}

// StoreName returns the file name a definitions file is kept under in a flat store: the name the
// file declares (its catalog, or its library of predicates) and its suffix, so that two versions
// of the same definitions are one file, as two versions of a rule are. The declared name admits
// no path separator, so the result is a safe single path component.
func (h Head) StoreName() (string, error) {
	switch {
	case !h.Kind.Definitions():
		return "", fmt.Errorf("not a definitions file (want %s or %s)", CatalogFileSuffix, PredicatesFileSuffix)
	case !ValidID(h.Name):
		return "", fmt.Errorf("the definitions have no name (%s)", h)
	}
	return h.Name + h.Kind.Suffix(), nil
}

// PackFile is one file of a pack: its path within the pack (/-separated), its kind and its
// content.
type PackFile struct {
	Path string
	Kind FileKind
	Data []byte
}

// ReadPackFS returns the pack files in fsys, found anywhere under its root, sorted by path.
func ReadPackFS(fsys fs.FS) ([]PackFile, error) {
	var files []PackFile
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		kind, ok := PackFileKind(path)
		if d.IsDir() || !ok {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		files = append(files, PackFile{Path: path, Kind: kind, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Pack is a loaded rule pack: its rules, validated against its definitions, and what was set
// aside for needing a newer spec than this engine supports (rules, definitions, and the rules
// that call a predicate of definitions set aside).
type Pack struct {
	Rules []*Rule
	// Library is the pack's definitions; nil for a pack that has none.
	Library *Library
	Skipped []SkippedRule
}

// LoadPackDir loads the pack under root. A file it names, one set aside or one that fails, is
// named by root joined with its path within the pack. A predicate's Source is its file's path
// within the pack.
func LoadPackDir(root string) (*Pack, error) {
	files, err := ReadPackFS(os.DirFS(root))
	if err != nil {
		return nil, err
	}
	return loadPack(files, root)
}

// LoadPackFS loads the pack in fsys, naming files by their paths within it.
func LoadPackFS(fsys fs.FS) (*Pack, error) {
	files, err := ReadPackFS(fsys)
	if err != nil {
		return nil, err
	}
	return loadPack(files, "")
}

// loadPack loads a pack's files: its definitions first, then each rule, decoded and validated
// against them, once. A file that needs a newer spec than this engine supports is set aside, and
// so is a rule that calls a predicate of definitions set aside; any other failure fails the load,
// naming the file, and so does a rule id declared twice. Files are named under root.
func loadPack(files []PackFile, root string) (*Pack, error) {
	at := func(path string) string {
		if root == "" {
			return path
		}
		return filepath.Join(root, filepath.FromSlash(path))
	}
	var definitions []DefinitionFile
	for _, f := range files {
		if f.Kind.Definitions() {
			definitions = append(definitions, DefinitionFile{Name: f.Path, Data: f.Data})
		}
	}
	lib, skipped, err := NewLibrary(definitions)
	if err != nil {
		if root != "" {
			return nil, fmt.Errorf("%s: definitions: %w", root, err)
		}
		return nil, fmt.Errorf("definitions: %w", err)
	}
	for i := range skipped {
		skipped[i].Path = at(skipped[i].Path)
	}
	pack := &Pack{Library: lib, Skipped: skipped}
	seen := map[string]string{} // id -> path
	for _, f := range files {
		if f.Kind != RuleFile {
			continue
		}
		path := at(f.Path)
		rule, err := lib.decodeValid(path, f.Data)
		if unsupported, ok := AsUnsupportedSpec(err); ok {
			pack.Skipped = append(pack.Skipped, SkippedRule{Path: path, Err: unsupported})
			continue
		}
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[rule.ID]; dup {
			return nil, fmt.Errorf("duplicate rule id %q in %s and %s", rule.ID, prev, path)
		}
		seen[rule.ID] = path
		pack.Rules = append(pack.Rules, rule)
	}
	return pack, nil
}
