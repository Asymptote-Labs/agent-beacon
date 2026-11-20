// Package mcpmanaged pushes administrator-chosen MCP servers to every account on a machine, for
// Claude Code and Codex CLI, through each runtime's own machine-wide configuration. Both
// mechanisms add to what a person configures, never replace it:
//
//   - Claude Code reads `managedMcpServers` from its managed settings (Claude Code 2.1.259 and
//     later). Beacon writes one drop-in of its own, beacon-mcp-servers.json, in
//     managed-settings.d beside managed-settings.json. Servers there load alongside the person's
//     user, project and plugin servers. This is deliberately not managed-mcp.json, which takes
//     exclusive control and stops every other server from loading.
//   - Codex reads /etc/codex/config.toml as its system configuration layer, deep-merged under the
//     person's ~/.codex/config.toml table by table, so [mcp_servers.<name>] tables there join the
//     person's own. Beacon edits that file as text, owns only the tables that carry its marker
//     comment, and parses the result to prove nothing else changed.
//
// Only remote (https) servers are supported, because Claude Code's managed key accepts nothing
// else: no stdio command, no ${VAR} expansion. Both files are readable by every account, so a
// header value is visible to anyone on the machine.
package mcpmanaged

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Runtime names, as --harness takes them.
const (
	RuntimeClaude = "claude"
	RuntimeCodex  = "codex"
)

// Runtimes is every runtime managed servers are pushed to, in the order they are reported.
var Runtimes = []string{RuntimeClaude, RuntimeCodex}

// ClaudeDropInName is the managed-settings.d file Beacon owns whole.
const ClaudeDropInName = "beacon-mcp-servers.json"

// CodexMarker is the comment line inside each [mcp_servers.<name>] table Beacon wrote. A table
// without it is someone else's and is never changed.
const CodexMarker = "# Pushed by `beacon mcp managed`; Beacon replaces this table on every apply."

// Server is one remote MCP server.
type Server struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Declaration is the complete set of servers an administrator pushes. Applying it makes each
// runtime's managed servers exactly this set: a server left out is removed.
type Declaration struct {
	Servers map[string]Server `json:"servers"`
}

// Names returns the declared server names, sorted.
func (d Declaration) Names() []string {
	names := make([]string, 0, len(d.Servers))
	for name := range d.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// serverName is Claude Code's rule for managedMcpServers names, which is also a bare TOML key.
var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// headerName is an HTTP token (RFC 9110).
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// ParseDeclaration reads and checks a declaration. Unknown fields are refused, so a typo
// (`header`, `command`) fails loudly instead of pushing a server without it.
func ParseDeclaration(data []byte) (Declaration, error) {
	var d Declaration
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Declaration{}, fmt.Errorf("the server list is not valid: %w", err)
	}
	if dec.More() {
		return Declaration{}, errors.New("the server list is not valid: more than one JSON value")
	}
	if d.Servers == nil {
		return Declaration{}, errors.New(`the server list needs a "servers" object (it may be empty, to remove every pushed server)`)
	}
	for _, name := range d.Names() {
		if err := checkServer(name, d.Servers[name]); err != nil {
			return Declaration{}, err
		}
	}
	return d, nil
}

func checkServer(name string, s Server) error {
	if !serverName.MatchString(name) {
		return fmt.Errorf("server %q: a name is 1 to 64 letters, digits, hyphens or underscores", name)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("server %q: url must be an https URL (Claude Code refuses anything else for a managed server)", name)
	}
	if strings.Contains(s.URL, "${") {
		return fmt.Errorf("server %q: url cannot use ${VAR}; managed servers take literal values only", name)
	}
	for key, value := range s.Headers {
		if !headerName.MatchString(key) {
			return fmt.Errorf("server %q: %q is not a valid header name", name, key)
		}
		if strings.Contains(value, "${") {
			return fmt.Errorf("server %q: header %s cannot use ${VAR}; managed servers take literal values only", name, key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("server %q: header %s contains a line break", name, key)
		}
	}
	return nil
}

// Paths are the machine-wide files Beacon writes.
type Paths struct {
	ClaudeDropIn string
	CodexConfig  string
	// ClaudeManagedPrefs is the macOS managed preferences plist for Claude Code. When an MDM
	// profile delivers one, Claude Code by default reads that source instead of the files.
	ClaudeManagedPrefs string
	// CodexRequirements is Codex's requirements.toml, whose [mcp_servers] allowlist disables
	// every server it does not list, pushed ones included.
	CodexRequirements string
}

// DefaultPaths are the locations Claude Code and Codex read on this operating system.
func DefaultPaths() (Paths, error) {
	switch runtime.GOOS {
	case "darwin":
		return Paths{
			ClaudeDropIn:       filepath.Join("/Library/Application Support/ClaudeCode", "managed-settings.d", ClaudeDropInName),
			CodexConfig:        "/etc/codex/config.toml",
			ClaudeManagedPrefs: "/Library/Managed Preferences/com.anthropic.claudecode.plist",
			CodexRequirements:  "/etc/codex/requirements.toml",
		}, nil
	case "linux":
		return Paths{
			ClaudeDropIn:      filepath.Join("/etc/claude-code", "managed-settings.d", ClaudeDropInName),
			CodexConfig:       "/etc/codex/config.toml",
			CodexRequirements: "/etc/codex/requirements.toml",
		}, nil
	}
	return Paths{}, fmt.Errorf("pushing managed MCP servers is supported on macOS and Linux, not %s", runtime.GOOS)
}

// Action is what happened, or would happen, to one runtime's file.
type Action string

const (
	ActionWrite     Action = "write"     // the file now holds the declared servers
	ActionUnchanged Action = "unchanged" // it already did
	ActionRemove    Action = "remove"    // Beacon's servers were removed
	ActionAbsent    Action = "absent"    // nothing of Beacon's was there
	ActionFailed    Action = "failed"
)

// Result is one runtime's outcome.
type Result struct {
	Runtime string   `json:"runtime"`
	Path    string   `json:"path"`
	Action  Action   `json:"action"`
	Servers []string `json:"servers"`
	// Conflicts are declared servers left out because the file already has a server of that
	// name that Beacon did not write.
	Conflicts []string `json:"conflicts,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Err       error    `json:"-"`
}

// Options select what Apply touches.
type Options struct {
	Paths    Paths
	Runtimes []string // empty means every runtime
	DryRun   bool
}

func (o Options) wants(rt string) bool {
	if len(o.Runtimes) == 0 {
		return true
	}
	for _, r := range o.Runtimes {
		if r == rt {
			return true
		}
	}
	return false
}

// Apply makes each selected runtime's Beacon-pushed servers exactly the declaration. One
// runtime failing does not stop the other; its Result carries the error.
func Apply(opts Options, d Declaration) []Result {
	var out []Result
	if opts.wants(RuntimeClaude) {
		out = append(out, applyClaude(opts, d))
	}
	if opts.wants(RuntimeCodex) {
		out = append(out, applyCodex(opts, d))
	}
	return out
}

// Remove takes every server Beacon pushed out of the selected runtimes' files.
func Remove(opts Options) []Result {
	return Apply(opts, Declaration{Servers: map[string]Server{}})
}

// Pushed is one server Beacon pushed, as its file holds it now.
type Pushed struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// RuntimeStatus is what a runtime's file holds of Beacon's.
type RuntimeStatus struct {
	Runtime  string   `json:"runtime"`
	Path     string   `json:"path"`
	Servers  []Pushed `json:"servers"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Status reads what Beacon has pushed, without changing anything. Any account may run it: both
// files are world-readable.
func Status(opts Options) []RuntimeStatus {
	var out []RuntimeStatus
	if opts.wants(RuntimeClaude) {
		out = append(out, claudeStatus(opts.Paths))
	}
	if opts.wants(RuntimeCodex) {
		out = append(out, codexStatus(opts.Paths))
	}
	return out
}

// writeSystemFile replaces path atomically with text, readable by every account: these are
// configuration files the runtimes read as whichever person runs them. An existing file keeps its
// mode. A symlink is refused rather than replaced or followed, because it means another tool
// manages that file.
func writeSystemFile(path, text string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; Beacon does not write through it", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".beacon-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// readSystemFile returns a file's text, or "" and false when it does not exist.
func readSystemFile(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("%s is a symlink; Beacon does not write through it", path)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

func failed(rt, path string, err error) Result {
	return Result{Runtime: rt, Path: path, Action: ActionFailed, Detail: err.Error(), Err: err}
}
