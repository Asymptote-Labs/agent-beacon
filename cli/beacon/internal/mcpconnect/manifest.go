package mcpconnect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The manifest is how `disconnect` knows which entries are Beacon's. A comment in the config
// would not do: several of these files are rewritten by their own runtime (Claude Code rewrites
// ~/.claude.json constantly, and `codex mcp` re-serializes config.toml), which drops comments --
// the lesson the Kimi Code installer learned. An entry is Beacon's when the manifest has a record
// for that harness and file, and the entry there still points at the recorded URL.
//
// The manifest holds no secret. It records a URL, an environment variable's name, file paths and
// hashes; a token value is never written anywhere by this package.

// ManifestPath is the file under ~/.beacon that records what `beacon mcp connect` wrote.
const ManifestPath = ".beacon/mcp/connections.json"

const manifestSchemaVersion = 1

// Record is one entry Beacon wrote into one harness config.
type Record struct {
	Harness    string `json:"harness"`
	Path       string `json:"path"`
	ServerName string `json:"server_name"`
	URL        string `json:"url"`
	// Command is the executable a local stdio entry runs; empty for Beacon Cloud entries.
	Command  string   `json:"command,omitempty"`
	Auth     AuthMode `json:"auth"`
	TokenEnv string   `json:"token_env,omitempty"`
	Method   string   `json:"method"`
	// CreatedFile is set when the config did not exist before connect, so disconnect can remove a
	// file that holds nothing else.
	CreatedFile bool `json:"created_file,omitempty"`
	// CreatedParents counts the enclosing objects (such as "mcpServers") connect had to create.
	CreatedParents int `json:"created_parents,omitempty"`
	// CreatedInputs is set when connect created VS Code's "inputs" array.
	CreatedInputs bool `json:"created_inputs,omitempty"`
	// Backup, BeforeSHA256 and AfterSHA256 let disconnect restore the exact original bytes when
	// the file has not changed since connect wrote it.
	Backup       string    `json:"backup,omitempty"`
	BeforeSHA256 string    `json:"before_sha256,omitempty"`
	AfterSHA256  string    `json:"after_sha256,omitempty"`
	WrittenAt    time.Time `json:"written_at"`
}

type manifest struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []Record `json:"records"`
}

func manifestPath(home string) string {
	return filepath.Join(home, ManifestPath)
}

func loadManifest(home string) (*manifest, error) {
	return loadManifestAt(manifestPath(home))
}

func loadManifestAt(path string) (*manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &manifest{SchemaVersion: manifestSchemaVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if m.SchemaVersion != manifestSchemaVersion {
		return nil, fmt.Errorf("%s has schema version %d; this Beacon understands %d", path, m.SchemaVersion, manifestSchemaVersion)
	}
	return &m, nil
}

// RecordedURL is the URL `connect` last wrote, or "" when it has written nothing. It is what an
// offline status compares entries against: a URL derived from the ingest URL may not be the
// canonical one connect checked and wrote.
func RecordedURL(home string) string {
	m, err := loadManifest(home)
	if err != nil {
		return ""
	}
	var latest Record
	for _, r := range m.Records {
		if latest.URL == "" || r.WrittenAt.After(latest.WrittenAt) {
			latest = r
		}
	}
	return latest.URL
}

func (m *manifest) find(harness, path string) (Record, bool) {
	for _, r := range m.Records {
		if r.Harness == harness && r.Path == path {
			return r, true
		}
	}
	return Record{}, false
}

func (m *manifest) put(rec Record) {
	m.drop(rec.Harness, rec.Path)
	m.Records = append(m.Records, rec)
	sort.Slice(m.Records, func(i, j int) bool {
		if m.Records[i].Harness != m.Records[j].Harness {
			return m.Records[i].Harness < m.Records[j].Harness
		}
		return m.Records[i].Path < m.Records[j].Path
	})
}

func (m *manifest) drop(harness, path string) {
	kept := m.Records[:0]
	for _, r := range m.Records {
		if r.Harness != harness || r.Path != path {
			kept = append(kept, r)
		}
	}
	m.Records = kept
}

// save writes the manifest 0600 in a 0700 directory, like the rest of ~/.beacon. An empty
// manifest is removed rather than left behind.
func (m *manifest) save(home string) error {
	return m.saveAt(manifestPath(home))
}

func (m *manifest) saveAt(path string) error {
	if len(m.Records) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	cf := configFile{path: path, real: path, mode: 0o600}
	return cf.write(string(data) + "\n")
}
