package mcpmanaged

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/jsonc"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tomledit"
)

const codexTable = "mcp_servers"

// CodexPrefix goes in front of every pushed server's name in Codex. Codex merges the system config
// into each person's ~/.codex/config.toml table by table, so a pushed [mcp_servers.linear] would
// merge into a person's own stdio [mcp_servers.linear], and a table with both `command` and `url`
// makes Codex refuse to load that person's whole config ("url is not supported for stdio"). A name
// of Beacon's own keeps pushed tables apart from anything a person configured. Claude Code keeps the
// plain name: it ranks a managed server above a same-named personal one instead of merging them.
const CodexPrefix = "org-"

// CodexName is the name a declared server has in Codex.
func CodexName(name string) string { return CodexPrefix + name }

// codexOwned returns the [mcp_servers.<name>] tables in text that carry Beacon's marker.
func codexOwned(text string) []string {
	var owned []string
	current := ""
	for _, line := range strings.Split(text, "\n") {
		if key, array, ok := tomledit.Header(line); ok {
			current = ""
			if !array && len(key) == 2 && key[0] == codexTable {
				current = key[1]
			}
			continue
		}
		if current != "" && strings.TrimSpace(line) == CodexMarker {
			owned = append(owned, current)
			current = ""
		}
	}
	sort.Strings(owned)
	return owned
}

// removeOwnedTable removes Beacon's [mcp_servers.<name>] table and its subtables. A block runs to
// the next header, but the comment lines directly above that header introduce the next table, not
// Beacon's (another tool may append `# CI profile` and [profiles.ci] after Beacon's table), so they
// are kept along with everything after them, as are comments after Beacon's last table. Blank
// lines that only separated the blocks go with Beacon's. A block at the end of the document also
// takes the blank lines above it, which is what tomledit.Append added.
func removeOwnedTable(text, name string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	start := -1
	for i, line := range lines {
		if key, array, ok := tomledit.Header(line); ok && !array && len(key) == 2 && key[0] == codexTable && key[1] == name {
			start = i
			break
		}
	}
	if start < 0 {
		return text, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		key, _, ok := tomledit.Header(lines[i])
		if !ok {
			continue
		}
		if len(key) > 2 && key[0] == codexTable && key[1] == name {
			continue // a subtable of Beacon's table
		}
		end = i
		break
	}
	// Walk back over the comment and blank lines that end the block; keep from the first comment of
	// that run that is not Beacon's marker. Before a header they introduce the next table, and at
	// the end of the document they are someone's notes; either way they are not Beacon's.
	keep := end
	for i := end - 1; i > start; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
		if strings.HasPrefix(trimmed, "#") && trimmed != CodexMarker {
			keep = i
		}
	}
	out := append(append([]string{}, lines[:start]...), lines[keep:]...)
	if keep == len(lines) {
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
	}
	return strings.Join(out, ""), true
}

// codexBlock renders one server's table. Names are bare keys (serverName guarantees it); values
// are TOML basic strings, which JSON string escapes are valid in.
func codexBlock(name string, s Server, eol string) string {
	var b strings.Builder
	b.WriteString("[" + codexTable + "." + name + "]" + eol)
	b.WriteString(CodexMarker + eol)
	b.WriteString("url = " + jsonc.Quote(s.URL) + eol)
	if len(s.Headers) > 0 {
		keys := make([]string, 0, len(s.Headers))
		for k := range s.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = jsonc.Quote(k) + " = " + jsonc.Quote(s.Headers[k])
		}
		b.WriteString("http_headers = { " + strings.Join(parts, ", ") + " }" + eol)
	}
	return b.String()
}

func codexEntry(s Server) map[string]any {
	e := map[string]any{"url": s.URL}
	if len(s.Headers) > 0 {
		h := map[string]any{}
		for k, v := range s.Headers {
			h[k] = v
		}
		e["http_headers"] = h
	}
	return e
}

func parseTOML(text string) (map[string]any, error) {
	parsed := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return parsed, nil
	}
	if err := toml.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// planCodex returns the new text of the Codex system config, the Codex names written, and those
// left out because the file already has a table of that name Beacon did not write. Every table
// Beacon wrote before is removed first, whatever its name, so a table from before CodexPrefix
// existed is replaced too.
func planCodex(text string, d Declaration) (updated string, written, conflicts []string, err error) {
	parsed, err := parseTOML(text)
	if err != nil {
		return "", nil, nil, fmt.Errorf("not valid TOML (%v)", err)
	}
	var servers map[string]any
	if raw, ok := parsed[codexTable]; ok {
		if servers, ok = raw.(map[string]any); !ok {
			return "", nil, nil, fmt.Errorf("%q is not a table", codexTable)
		}
	}
	owned := codexOwned(text)
	isOwned := map[string]bool{}
	for _, name := range owned {
		isOwned[name] = true
	}
	eol := tomledit.LineEnding(text, "\n")
	updated = text
	for _, name := range owned {
		var found bool
		if updated, found = removeOwnedTable(updated, name); !found {
			return "", nil, nil, fmt.Errorf("Beacon's [%s.%s] table could not be removed", codexTable, name)
		}
	}
	if len(owned) > 0 {
		updated = tomledit.EndWithNewline(updated, eol)
	}
	var blocks []string
	for _, declared := range d.Names() {
		name := CodexName(declared)
		if _, taken := servers[name]; taken && !isOwned[name] {
			conflicts = append(conflicts, name)
			continue
		}
		written = append(written, name)
		blocks = append(blocks, codexBlock(name, d.Servers[declared], eol))
	}
	if len(blocks) > 0 {
		updated = tomledit.Append(updated, strings.Join(blocks, eol), eol)
	}
	if err := verifyCodex(parsed, updated, owned, written, d); err != nil {
		return "", nil, nil, err
	}
	return updated, written, conflicts, nil
}

// verifyCodex parses the proposed text and checks that it is the original with exactly Beacon's
// tables replaced: every other key, at every depth, deeply equal.
func verifyCodex(before map[string]any, after string, removed, written []string, d Declaration) error {
	a, err := parseTOML(after)
	if err != nil {
		return fmt.Errorf("Beacon's edit would not parse (%v)", err)
	}
	want := normalize(before)
	if servers, ok := want[codexTable].(map[string]any); ok {
		for _, name := range removed {
			delete(servers, name)
		}
	}
	if len(written) > 0 {
		servers, ok := want[codexTable].(map[string]any)
		if !ok {
			servers = map[string]any{}
			want[codexTable] = servers
		}
		for _, name := range written {
			servers[name] = normalizeValue(codexEntry(d.Servers[strings.TrimPrefix(name, CodexPrefix)]))
		}
	}
	got := normalize(a)
	// [mcp_servers] exists in TOML only through its subtables, so with Beacon's removed and no
	// others the key is gone; an empty table and a missing one are the same configuration.
	for _, doc := range []map[string]any{want, got} {
		if m, ok := doc[codexTable].(map[string]any); ok && len(m) == 0 {
			delete(doc, codexTable)
		}
	}
	if !reflect.DeepEqual(want, got) {
		return errors.New("Beacon's edit would have changed more than its own servers; the file was left unchanged")
	}
	return nil
}

func normalize(doc map[string]any) map[string]any {
	m, _ := normalizeValue(doc).(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// normalizeValue makes decodings comparable (go-toml decodes integers as int64 and dates as
// time values).
func normalizeValue(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}

func applyCodex(opts Options, d Declaration) Result {
	path := opts.Paths.CodexConfig
	text, _, err := readSystemFile(path)
	if err != nil {
		return failed(RuntimeCodex, path, err)
	}
	updated, written, conflicts, err := planCodex(text, d)
	if err != nil {
		return failed(RuntimeCodex, path, fmt.Errorf("%s: %w", path, err))
	}
	res := Result{Runtime: RuntimeCodex, Path: path, Servers: written, Conflicts: conflicts}
	if res.Servers == nil {
		res.Servers = []string{}
	}
	if len(conflicts) > 0 {
		res.Detail = fmt.Sprintf("%s already has %s that Beacon did not write; left alone", path, strings.Join(conflicts, ", "))
	}
	switch {
	case updated == text && len(written) == 0 && len(codexOwned(text)) == 0:
		res.Action = ActionAbsent
		return res
	case updated == text:
		res.Action = ActionUnchanged
		return res
	case len(written) == 0:
		res.Action = ActionRemove
	default:
		res.Action = ActionWrite
	}
	if opts.DryRun {
		return res
	}
	if err := writeSystemFile(path, updated); err != nil {
		return failed(RuntimeCodex, path, err)
	}
	return res
}

func codexStatus(p Paths) RuntimeStatus {
	st := RuntimeStatus{Runtime: RuntimeCodex, Path: p.CodexConfig, Servers: []Pushed{}}
	text, _, err := readSystemFile(p.CodexConfig)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	parsed, err := parseTOML(text)
	if err != nil {
		st.Error = fmt.Sprintf("%s is not valid TOML (%v)", p.CodexConfig, err)
		return st
	}
	servers, _ := parsed[codexTable].(map[string]any)
	for _, name := range codexOwned(text) {
		entry, _ := servers[name].(map[string]any)
		u, _ := entry["url"].(string)
		st.Servers = append(st.Servers, Pushed{Name: name, URL: u})
	}
	if len(st.Servers) > 0 && p.CodexRequirements != "" {
		if req, _, err := readSystemFile(p.CodexRequirements); err == nil {
			if doc, err := parseTOML(req); err == nil {
				if _, ok := doc[codexTable]; ok {
					st.Warnings = append(st.Warnings, p.CodexRequirements+" has an [mcp_servers] allowlist; Codex disables every server it does not list, pushed ones included")
				}
			}
		}
	}
	return st
}
