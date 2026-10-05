package mcpconnect

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// The local stdio server, `beacon mcp serve`, reads this machine's runtime log. Onboarding
// registers it in every detected harness unless the person opts out, so an agent can search its
// own history whichever destination was chosen. It is a different entry from Beacon Cloud MCP
// (ServerName) and has its own manifest, so `beacon mcp connect` and `disconnect` never touch it
// and removing it never touches theirs.
//
// The policy is narrower than connect's: Beacon adds the entry where the name is free and never
// replaces one that is already there, whoever wrote it. Nothing here touches the network.

// LocalServerName is the name the local stdio server is registered under.
const LocalServerName = "beacon"

// LocalManifestPath is the file under the home directory that records the local entries Beacon
// wrote.
const LocalManifestPath = ".beacon/mcp/local.json"

// LocalOptions configures registering the local server.
type LocalOptions struct {
	Home string
	// Command is the beacon executable the harness runs, as an absolute path: harnesses started
	// from a desktop launcher do not see a shell's PATH.
	Command string

	LookPath func(string) (string, error)
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (o LocalOptions) connectOptions() Options {
	return Options{Home: o.Home, LookPath: o.LookPath, Run: o.Run}
}

var timeNow = time.Now

func localArgs() []any {
	return []any{"mcp", "serve"}
}

// LocalSupported reports whether Beacon can register the local server in t.
func (t Target) LocalSupported() bool {
	return t.Automatic && t.localEntry != nil
}

// InstallLocal registers the local server in each target that supports it. Each target is
// independent: a failure is reported on its item and the rest still run.
func InstallLocal(ctx context.Context, opts LocalOptions, targets []Target) ([]Item, error) {
	if strings.TrimSpace(opts.Command) == "" {
		return nil, errors.New("the beacon executable path is required")
	}
	manifestFile := filepath.Join(opts.Home, LocalManifestPath)
	m, err := loadManifestAt(manifestFile)
	if err != nil {
		return nil, err
	}
	copts := opts.connectOptions()
	var items []Item
	for _, t := range targets {
		if !t.LocalSupported() {
			continue
		}
		it := Item{Target: t, Method: "file"}
		path, err := t.path(opts.Home)
		if err != nil {
			it.Action, it.Detail = ActionSkip, err.Error()
			items = append(items, it)
			continue
		}
		it.Path = path
		st, err := inspectNamed(t, path, LocalServerName)
		if err != nil {
			it.Action, it.Detail = ActionSkip, err.Error()
			items = append(items, it)
			continue
		}
		if st.exists {
			it.Action, it.Detail = ActionPresent, "an entry named "+LocalServerName+" is already configured; left as is"
			items = append(items, it)
			continue
		}
		it.Action = ActionAdd
		if t.cli != "" {
			if cliPath, err := copts.lookPath(t.cli); err == nil {
				it.cliPath, it.Method = cliPath, t.cli+" CLI"
			}
		}
		rec, err := addLocal(ctx, opts, it, st)
		if err != nil {
			it.Err = err
			items = append(items, it)
			continue
		}
		m.put(rec)
		if err := m.saveAt(manifestFile); err != nil {
			it.Err = fmt.Errorf("the entry was written, but the record of it could not be saved (%w); `beacon mcp uninstall` will not remove it", err)
		}
		items = append(items, it)
	}
	return items, nil
}

func addLocal(ctx context.Context, opts LocalOptions, it Item, st state) (Record, error) {
	t := it.Target
	entry := t.localEntry(opts.Command)
	rec := Record{Harness: t.Name, Path: it.Path, ServerName: LocalServerName, Command: opts.Command, Method: "file"}
	if it.cliPath != "" {
		rec.Method = t.cli + "-cli"
		backup, err := st.file.backup(timeNow())
		if err != nil {
			return Record{}, fmt.Errorf("could not back up %s: %w", it.Path, err)
		}
		rec.Backup = backup
		args := []string{"mcp", "add", "--scope", "user", LocalServerName, "--", opts.Command}
		for _, a := range localArgs() {
			args = append(args, fmt.Sprint(a))
		}
		if out, err := opts.connectOptions().run(ctx, it.cliPath, args...); err != nil {
			return Record{}, fmt.Errorf("`%s mcp add` failed: %v: %s", t.cli, err, strings.TrimSpace(string(out)))
		}
		after, err := inspectNamed(t, it.Path, LocalServerName)
		if err != nil {
			return Record{}, err
		}
		if !after.exists || !sameLocalCommand(t, after.entry, opts.Command) {
			return Record{}, fmt.Errorf("`%s mcp add` ran, but %s does not hold the expected %s entry", t.cli, it.Path, LocalServerName)
		}
		return rec, nil
	}

	cf := st.file
	var updated string
	if t.format == formatTOML {
		eol := tomlLineEnding(cf.text)
		updated = appendTOMLBlock(cf.text, tomlBlock(t.container[0], LocalServerName, entry, eol), eol)
	} else {
		updated = cf.text
		if strings.TrimSpace(updated) == "" && len(t.skeleton) > 0 {
			updated = renderJSONDocument(t.skeleton, detectJSONStyle(cf.text))
		}
		var err error
		if updated, rec.CreatedParents, err = insertJSONMember(updated, t.container, LocalServerName, entry); err != nil {
			return Record{}, fmt.Errorf("could not edit %s: %w", it.Path, err)
		}
	}
	b, a, err := parseBoth(t, cf.text, updated)
	if err != nil {
		return Record{}, fmt.Errorf("%s: %w; the file was left unchanged", it.Path, err)
	}
	if strings.TrimSpace(cf.text) == "" {
		for _, f := range t.skeleton {
			b[f.Key] = normalizeJSON(f.Value)
		}
	}
	setPath(b, t.container, LocalServerName, normalizeJSON(entry))
	if !reflect.DeepEqual(normalizeValue(b), normalizeValue(a)) {
		return Record{}, fmt.Errorf("%s: Beacon's edit would have changed more than its own %s entry; the file was left unchanged", it.Path, LocalServerName)
	}
	backup, err := cf.backup(timeNow())
	if err != nil {
		return Record{}, fmt.Errorf("could not back up %s: %w", it.Path, err)
	}
	rec.Backup, rec.CreatedFile = backup, !cf.exists
	if err := cf.write(updated); err != nil {
		return Record{}, fmt.Errorf("could not write %s: %w", it.Path, err)
	}
	rec.AfterSHA256 = digest(updated)
	return rec, nil
}

// sameLocalCommand reports whether an entry runs command with the local server's arguments,
// whatever else the harness added to it (Claude Code writes an empty "env").
func sameLocalCommand(t Target, entry map[string]any, command string) bool {
	want, _ := normalizeJSON(t.localEntry(command)).(map[string]any)
	for _, key := range []string{"command", "args"} {
		if !reflect.DeepEqual(normalizeValue(entry[key]), normalizeValue(want[key])) {
			return false
		}
	}
	return true
}

// UninstallLocal removes the local entries Beacon recorded, and only while each still runs the
// command Beacon wrote: an entry someone has since changed is theirs.
func UninstallLocal(ctx context.Context, opts LocalOptions) ([]Item, error) {
	manifestFile := filepath.Join(opts.Home, LocalManifestPath)
	m, err := loadManifestAt(manifestFile)
	if err != nil {
		return nil, err
	}
	copts := opts.connectOptions()
	var items []Item
	for _, rec := range append([]Record(nil), m.Records...) {
		t, ok := Lookup(rec.Harness)
		if !ok || !t.LocalSupported() {
			continue
		}
		it := Item{Target: t, Path: rec.Path, Method: rec.Method}
		it.Action, it.Detail, it.Err = removeLocal(ctx, copts, t, rec)
		if it.Err == nil {
			m.drop(rec.Harness, rec.Path)
			if err := m.saveAt(manifestFile); err != nil {
				it.Err = err
			}
		}
		items = append(items, it)
	}
	return items, nil
}

func removeLocal(ctx context.Context, opts Options, t Target, rec Record) (Action, string, error) {
	st, err := inspectNamed(t, rec.Path, LocalServerName)
	if err != nil {
		return ActionSkip, "", err
	}
	if !st.exists {
		return ActionAbsent, "the entry is already gone", nil
	}
	if !sameLocalCommand(t, st.entry, rec.Command) {
		return ActionAbsent, "the " + LocalServerName + " entry was changed after Beacon wrote it; left alone", nil
	}
	if rec.Method == t.cli+"-cli" && t.cli != "" {
		if cliPath, err := opts.lookPath(t.cli); err == nil {
			if out, err := opts.run(ctx, cliPath, "mcp", "remove", LocalServerName, "--scope", "user"); err != nil {
				return ActionSkip, "", fmt.Errorf("`%s mcp remove` failed: %v: %s", t.cli, err, strings.TrimSpace(string(out)))
			}
			after, err := inspectNamed(t, rec.Path, LocalServerName)
			if err != nil {
				return ActionSkip, "", err
			}
			if after.exists {
				return ActionSkip, "", fmt.Errorf("`%s mcp remove` ran, but %s still holds %s", t.cli, rec.Path, LocalServerName)
			}
			return ActionRemove, "removed with `" + t.cli + " mcp remove`", nil
		}
	}
	cf := st.file
	var updated string
	if t.format == formatTOML {
		stripped, found := stripTOMLTable(cf.text, t.container[0], LocalServerName)
		if !found {
			return ActionSkip, "", errors.New("the " + LocalServerName + " entry is not a [" + t.container[0] + "." + LocalServerName + "] table; remove it by hand")
		}
		updated = endTOMLWithNewline(stripped, tomlLineEnding(cf.text))
	} else {
		if updated, err = removeJSONMember(cf.text, t.container, LocalServerName, rec.CreatedParents); err != nil {
			return ActionSkip, "", fmt.Errorf("could not edit %s: %w", rec.Path, err)
		}
	}
	b, a, err := parseBoth(t, cf.text, updated)
	if err != nil {
		return ActionSkip, "", err
	}
	deletePath(b, t.container, LocalServerName, rec.CreatedParents)
	if t.format == formatTOML {
		dropEmptyTable(b, t.container[0])
		dropEmptyTable(a, t.container[0])
	}
	if !reflect.DeepEqual(normalizeValue(b), normalizeValue(a)) {
		return ActionSkip, "", fmt.Errorf("%s: Beacon's edit would have changed more than its own %s entry; the file was left unchanged", rec.Path, LocalServerName)
	}
	if rec.CreatedFile && onlySkeleton(t, updated) {
		if err := cf.remove(); err != nil {
			return ActionSkip, "", err
		}
		return ActionRemove, "removed the file Beacon created", nil
	}
	if _, err := cf.backup(timeNow()); err != nil {
		return ActionSkip, "", fmt.Errorf("could not back up %s: %w", rec.Path, err)
	}
	if err := cf.write(updated); err != nil {
		return ActionSkip, "", err
	}
	return ActionRemove, "removed Beacon's entry", nil
}
