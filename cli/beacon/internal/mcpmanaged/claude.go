package mcpmanaged

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// claudeEntry is one managedMcpServers entry. Claude Code accepts only remote servers there.
type claudeEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type claudeDropIn struct {
	ManagedMcpServers map[string]claudeEntry `json:"managedMcpServers"`
}

// renderClaude is the drop-in for a declaration. encoding/json sorts map keys, so the same
// declaration always renders the same bytes and a re-apply is a no-op.
func renderClaude(d Declaration) string {
	doc := claudeDropIn{ManagedMcpServers: map[string]claudeEntry{}}
	for name, s := range d.Servers {
		doc.ManagedMcpServers[name] = claudeEntry{Type: "http", URL: s.URL, Headers: s.Headers}
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	return string(data) + "\n"
}

func applyClaude(opts Options, d Declaration) Result {
	path := opts.Paths.ClaudeDropIn
	current, exists, err := readSystemFile(path)
	if err != nil {
		return failed(RuntimeClaude, path, err)
	}
	if len(d.Servers) == 0 {
		if !exists {
			return Result{Runtime: RuntimeClaude, Path: path, Action: ActionAbsent, Servers: []string{}}
		}
		if !opts.DryRun {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return failed(RuntimeClaude, path, err)
			}
		}
		return Result{Runtime: RuntimeClaude, Path: path, Action: ActionRemove, Servers: []string{}}
	}
	want := renderClaude(d)
	res := Result{Runtime: RuntimeClaude, Path: path, Servers: d.Names()}
	if exists && current == want {
		res.Action = ActionUnchanged
		return res
	}
	if !opts.DryRun {
		if err := writeSystemFile(path, want); err != nil {
			return failed(RuntimeClaude, path, err)
		}
	}
	res.Action = ActionWrite
	return res
}

func claudeStatus(p Paths) RuntimeStatus {
	st := RuntimeStatus{Runtime: RuntimeClaude, Path: p.ClaudeDropIn, Servers: []Pushed{}}
	text, exists, err := readSystemFile(p.ClaudeDropIn)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if exists {
		var doc claudeDropIn
		if err := json.Unmarshal([]byte(text), &doc); err != nil {
			st.Error = fmt.Sprintf("%s is not valid JSON (%v)", p.ClaudeDropIn, err)
			return st
		}
		for name, e := range doc.ManagedMcpServers {
			st.Servers = append(st.Servers, Pushed{Name: name, URL: e.URL})
		}
		sort.Slice(st.Servers, func(i, j int) bool { return st.Servers[i].Name < st.Servers[j].Name })
	}
	if p.ClaudeManagedPrefs != "" {
		if _, err := os.Stat(p.ClaudeManagedPrefs); err == nil && len(st.Servers) > 0 {
			st.Warnings = append(st.Warnings, "a configuration profile delivers Claude Code managed preferences ("+p.ClaudeManagedPrefs+"); Claude Code then ignores managed-settings files, including Beacon's, unless that profile sets managedSourcesBehavior to \"merge\"")
		}
	}
	return st
}
