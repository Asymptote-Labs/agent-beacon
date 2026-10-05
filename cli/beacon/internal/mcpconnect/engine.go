package mcpconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

// Action is what connect will do, or did, for one target.
type Action string

const (
	ActionAdd      Action = "add"      // no entry yet; Beacon adds one
	ActionUpdate   Action = "update"   // Beacon's own entry, with a different URL or auth mode
	ActionPresent  Action = "present"  // already configured as asked; nothing to do
	ActionConflict Action = "conflict" // an entry Beacon did not write, or with another URL; left alone
	ActionReplace  Action = "replace"  // a conflict that --force overwrites
	ActionManual   Action = "manual"   // Beacon writes nothing; steps are printed
	ActionSkip     Action = "skip"     // cannot be handled; Detail says why
	ActionRemove   Action = "remove"   // disconnect: Beacon's entry is removed
	ActionAbsent   Action = "absent"   // disconnect: nothing of Beacon's is there
)

// Options configures a plan.
type Options struct {
	Home     string
	URL      string
	TokenEnv string // empty for OAuth
	Force    bool

	// LookPath finds a harness CLI (claude); nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Run runs a harness CLI and returns its combined output; nil runs it for real.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	Now func() time.Time
}

func (o Options) auth() AuthMode {
	if o.TokenEnv != "" {
		return AuthTokenEnv
	}
	return AuthOAuth
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) lookPath(name string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath(name)
	}
	return exec.LookPath(name)
}

func (o Options) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if o.Run != nil {
		return o.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Item is one row of a plan, and after Apply, of its result.
type Item struct {
	Target Target
	Path   string
	Action Action
	Auth   AuthMode
	Method string // "file" or "<cli> CLI"
	Detail string
	// ExistingURL is the URL of an entry already there.
	ExistingURL string
	// Err is set when applying this item failed; other items still ran.
	Err error

	cliPath string
}

// NextStep is the follow-up the person runs after connect.
func (it Item) NextStep(opts Options) string {
	if it.Action == ActionManual {
		return it.Target.ManualSteps(opts.URL)
	}
	return it.Target.NextStep(it.Auth, opts.TokenEnv)
}

// Writes reports whether applying the item changes a file.
func (it Item) Writes() bool {
	return it.Action == ActionAdd || it.Action == ActionUpdate || it.Action == ActionReplace
}

// state is what a config currently holds for the server name.
type state struct {
	file     configFile
	exists   bool           // the entry exists
	entry    map[string]any // its decoded value
	url      string
	inputOK  bool // VS Code: the input Beacon's token mode needs is absent or already Beacon's
	inputHas bool // VS Code: an input with Beacon's id exists
}

func inspect(t Target, path string) (state, error) {
	var st state
	cf, err := readConfig(path)
	if err != nil {
		return st, err
	}
	st.file = cf
	st.inputOK = true
	switch t.format {
	case formatTOML:
		parsed, err := parseTOML(cf.text)
		if err != nil {
			return st, fmt.Errorf("%s is not valid TOML (%v)", path, err)
		}
		if raw, ok := parsed[t.container[0]]; ok {
			if _, isTable := raw.(map[string]any); !isTable {
				return st, fmt.Errorf("%s: %q is not a table", path, t.container[0])
			}
		}
		st.entry, st.exists = tomlTableEntry(parsed, t.container[0], ServerName)
	default:
		parsed, err := decodeJSONC(cf.text)
		if err != nil {
			return st, fmt.Errorf("%s is not valid JSON (%v)", path, err)
		}
		root, _ := parseJSONC(cf.text)
		if root != nil {
			chain, err := lookupObject(root, t.container)
			if err != nil {
				return st, fmt.Errorf("%s: %v", path, err)
			}
			if len(chain) == len(t.container)+1 && chain[len(chain)-1].memberCount(ServerName) > 1 {
				return st, fmt.Errorf("%s: %q appears more than once", path, ServerName)
			}
		}
		container := dig(parsed, t.container)
		if raw, ok := container[ServerName]; ok {
			st.exists = true
			st.entry, _ = raw.(map[string]any)
			if st.entry == nil {
				st.entry = map[string]any{}
			}
		}
		if t.vscodeInputs {
			for _, input := range inputList(parsed) {
				if id, _ := input["id"].(string); id == vscodeInputID {
					st.inputHas = true
					st.inputOK = reflect.DeepEqual(input, normalizeJSON(vscodeInput()))
				}
			}
		}
	}
	if st.exists {
		st.url, _ = st.entry["url"].(string)
	}
	return st, nil
}

func dig(doc map[string]any, path []string) map[string]any {
	cur := doc
	for _, seg := range path {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		cur = next
	}
	return cur
}

func inputList(doc map[string]any) []map[string]any {
	raw, _ := doc["inputs"].([]any)
	var out []map[string]any
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func vscodeInput() ordered {
	return ordered{
		{"type", "promptString"},
		{"id", vscodeInputID},
		{"description", "Beacon Cloud MCP token (" + TokenPage + ")"},
		{"password", true},
	}
}

// normalizeJSON turns an ordered value into what decodeJSONC produces for it.
func normalizeJSON(v any) any {
	text := renderJSON(v, jsonStyle{unit: "  ", eol: "\n"}, 0, true, ":")
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var out any
	_ = decoder.Decode(&out)
	return out
}

func sameURL(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// Plan works out what connect would do for each target without writing anything.
func Plan(opts Options, targets []Target) ([]Item, error) {
	m, err := loadManifest(opts.Home)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, t := range targets {
		items = append(items, planOne(opts, m, t))
	}
	return items, nil
}

func planOne(opts Options, m *manifest, t Target) Item {
	it := Item{Target: t, Auth: opts.auth(), Method: "file"}
	if !t.Automatic {
		it.Action, it.Auth, it.Method = ActionManual, AuthManual, ""
		it.Detail = "no confirmed MCP OAuth support; Beacon writes nothing"
		return it
	}
	path, err := t.path(opts.Home)
	if err != nil {
		it.Action, it.Detail = ActionSkip, err.Error()
		return it
	}
	it.Path = path
	if t.cli != "" {
		if cliPath, err := opts.lookPath(t.cli); err == nil {
			it.cliPath, it.Method = cliPath, t.cli+" CLI"
		}
	}
	st, err := inspect(t, path)
	if err != nil {
		it.Action, it.Detail = ActionSkip, err.Error()
		return it
	}
	it.ExistingURL = st.url
	rec, recorded := m.find(t.Name, path)
	ours := recorded && st.exists && sameURL(st.url, rec.URL)
	want := normalizeJSON(t.entry(opts.URL, opts.TokenEnv))
	same := st.exists && reflect.DeepEqual(normalizeJSON(st.entry), want)
	if opts.TokenEnv != "" && t.vscodeInputs && !st.inputOK && !(recorded && rec.Auth == AuthTokenEnv) {
		if opts.Force {
			it.Action, it.Detail = ActionReplace, "an input named "+vscodeInputID+" exists; --force replaces it"
		} else {
			it.Action, it.Detail = ActionConflict, "an input named "+vscodeInputID+" exists that Beacon did not write; pass --force to replace it"
		}
		return it
	}
	switch {
	case !st.exists:
		it.Action = ActionAdd
	case ours && same && (!t.vscodeInputs || opts.TokenEnv == "" || st.inputHas):
		it.Action, it.Detail = ActionPresent, "already configured by Beacon"
	case ours:
		it.Action, it.Detail = ActionUpdate, "Beacon's entry is updated to the requested URL and auth mode"
	case same:
		it.Action, it.Detail = ActionPresent, "already configured outside Beacon; left as is"
	case opts.Force:
		it.Action, it.Detail = ActionReplace, conflictDetail(st, opts.URL)+"; --force replaces it"
	default:
		it.Action, it.Detail = ActionConflict, conflictDetail(st, opts.URL)+"; left alone (pass --force to replace it)"
	}
	return it
}

func conflictDetail(st state, url string) string {
	if st.url != "" && !sameURL(st.url, url) {
		return fmt.Sprintf("an entry named %s points at %s", ServerName, st.url)
	}
	return fmt.Sprintf("an entry named %s exists that Beacon did not write", ServerName)
}

// Apply carries out the writing items of a plan. Each target is independent: one that fails is
// reported in its Err and the rest still run. The manifest is saved after every write, so a later
// failure never loses the record of an earlier success.
func Apply(ctx context.Context, opts Options, items []Item) ([]Item, error) {
	m, err := loadManifest(opts.Home)
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = it
		if !it.Writes() {
			continue
		}
		rec, err := applyOne(ctx, opts, m, it)
		if err != nil {
			out[i].Err = err
			continue
		}
		m.put(rec)
		if err := m.save(opts.Home); err != nil {
			out[i].Err = fmt.Errorf("the entry was written, but the record of it could not be saved (%w); `beacon mcp disconnect` will not remove it", err)
		}
	}
	return out, nil
}

func applyOne(ctx context.Context, opts Options, m *manifest, it Item) (Record, error) {
	t := it.Target
	st, err := inspect(t, it.Path)
	if err != nil {
		return Record{}, err
	}
	rec := Record{
		Harness: t.Name, Path: it.Path, ServerName: ServerName, URL: opts.URL,
		Auth: opts.auth(), TokenEnv: opts.TokenEnv, Method: "file", WrittenAt: opts.now().UTC(),
	}
	prev, recorded := m.find(t.Name, it.Path)
	if it.cliPath != "" {
		return applyCLI(ctx, opts, it, st, rec)
	}
	cf := st.file
	updated, created, createdInputs, err := editForConnect(t, cf.text, st, opts)
	if err != nil {
		return Record{}, fmt.Errorf("could not edit %s: %w", it.Path, err)
	}
	if err := verifyConnect(t, cf.text, updated, opts); err != nil {
		return Record{}, fmt.Errorf("%s: %w; the file was left unchanged", it.Path, err)
	}
	// Keep the baseline of Beacon's first write while the file holds nothing but Beacon's
	// changes, so disconnect can still restore it byte for byte.
	if recorded && it.Action == ActionUpdate && digest(cf.text) == prev.AfterSHA256 {
		rec.CreatedFile, rec.CreatedParents, rec.CreatedInputs = prev.CreatedFile, prev.CreatedParents, prev.CreatedInputs
		rec.Backup, rec.BeforeSHA256 = prev.Backup, prev.BeforeSHA256
		if opts.TokenEnv == "" {
			rec.CreatedInputs = false
		}
		if createdInputs {
			rec.CreatedInputs = true
		}
	} else {
		backup, err := cf.backup(opts.now())
		if err != nil {
			return Record{}, fmt.Errorf("could not back up %s: %w", it.Path, err)
		}
		rec.Backup, rec.BeforeSHA256 = backup, digest(cf.text)
		rec.CreatedFile = !cf.exists
		rec.CreatedParents, rec.CreatedInputs = created, createdInputs
		if recorded && it.Action == ActionUpdate {
			rec.CreatedParents = max(rec.CreatedParents, prev.CreatedParents)
			rec.CreatedInputs = rec.CreatedInputs || (prev.CreatedInputs && opts.TokenEnv != "")
			// This backup still holds Beacon's previous entry, so it is not a state disconnect
			// may restore: that would put the old entry back and forget it. Keep the backup for
			// the person, but leave no baseline, so disconnect splices the entry out instead.
			rec.BeforeSHA256 = ""
		}
	}
	if err := cf.write(updated); err != nil {
		return Record{}, fmt.Errorf("could not write %s: %w", it.Path, err)
	}
	rec.AfterSHA256 = digest(updated)
	return rec, nil
}

// editForConnect returns the config text with Beacon's entry in place.
func editForConnect(t Target, text string, st state, opts Options) (updated string, created int, createdInputs bool, err error) {
	entry := t.entry(opts.URL, opts.TokenEnv)
	if t.format == formatTOML {
		eol := tomlLineEnding(text)
		stripped := text
		if st.exists {
			var found bool
			stripped, found = stripTOMLTable(text, t.container[0], ServerName)
			if !found {
				return "", 0, false, errors.New("the existing " + ServerName + " entry is not a [" + t.container[0] + "." + ServerName + "] table, so Beacon cannot replace it; remove it by hand")
			}
			stripped = endTOMLWithNewline(stripped, eol)
		}
		return appendTOMLBlock(stripped, tomlBlock(t.container[0], ServerName, entry, eol), eol), 0, false, nil
	}
	updated = text
	if strings.TrimSpace(updated) == "" && len(t.skeleton) > 0 {
		updated = renderJSONDocument(t.skeleton, detectJSONStyle(text))
	}
	if st.exists {
		if updated, err = removeJSONMember(updated, t.container, ServerName, 0); err != nil {
			return "", 0, false, err
		}
	}
	if updated, created, err = insertJSONMember(updated, t.container, ServerName, entry); err != nil {
		return "", 0, false, err
	}
	if t.vscodeInputs {
		if st.inputHas {
			if updated, err = removeJSONElements(updated, nil, "inputs", isBeaconInput, false); err != nil {
				return "", 0, false, err
			}
		}
		if opts.TokenEnv != "" {
			if updated, createdInputs, err = appendJSONElement(updated, nil, "inputs", vscodeInput()); err != nil {
				return "", 0, false, err
			}
		}
	}
	return updated, created, createdInputs, nil
}

func isBeaconInput(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	id, _ := m["id"].(string)
	return id == vscodeInputID
}

// verifyConnect parses the proposed text and checks that it is the original with exactly Beacon's
// entry set: every other key, at every depth, deeply equal. It is the safety net under the text
// splicing, the same check the Kimi Code installer runs before it replaces config.toml.
func verifyConnect(t Target, before, after string, opts Options) error {
	b, a, err := parseBoth(t, before, after)
	if err != nil {
		return err
	}
	if strings.TrimSpace(before) == "" && len(t.skeleton) > 0 {
		for _, f := range t.skeleton {
			b[f.Key] = normalizeJSON(f.Value)
		}
	}
	setPath(b, t.container, ServerName, normalizeJSON(t.entry(opts.URL, opts.TokenEnv)))
	if t.vscodeInputs {
		dropBeaconInputs(b)
		if opts.TokenEnv != "" {
			list, _ := b["inputs"].([]any)
			b["inputs"] = append(list, normalizeJSON(vscodeInput()))
		}
	}
	if !reflect.DeepEqual(normalizeValue(b), normalizeValue(a)) {
		return errors.New("Beacon's edit would have changed more than its own " + ServerName + " entry")
	}
	return nil
}

// verifyDisconnect checks that after is before without Beacon's entry (and, when Beacon created
// them, the containers the entry leaves empty).
func verifyDisconnect(t Target, before, after string, rec Record) error {
	b, a, err := parseBoth(t, before, after)
	if err != nil {
		return err
	}
	deletePath(b, t.container, ServerName, rec.CreatedParents)
	if t.format == formatTOML {
		// [mcp_servers] exists in TOML only through its subtables, so once Beacon's table is
		// stripped a document with no other server has no mcp_servers key at all. An explicit
		// empty [mcp_servers] header survives the strip. Either way an empty table and a missing
		// one are the same configuration.
		dropEmptyTable(b, t.container[0])
		dropEmptyTable(a, t.container[0])
	}
	if t.vscodeInputs && rec.Auth == AuthTokenEnv {
		dropBeaconInputs(b)
		if list, ok := b["inputs"].([]any); ok && len(list) == 0 && rec.CreatedInputs {
			delete(b, "inputs")
		}
	}
	if !reflect.DeepEqual(normalizeValue(b), normalizeValue(a)) {
		return errors.New("Beacon's edit would have changed more than its own " + ServerName + " entry")
	}
	return nil
}

func dropEmptyTable(doc map[string]any, key string) {
	if m, ok := doc[key].(map[string]any); ok && len(m) == 0 {
		delete(doc, key)
	}
}

func parseBoth(t Target, before, after string) (map[string]any, map[string]any, error) {
	parse := decodeJSONC
	if t.format == formatTOML {
		parse = parseTOML
	}
	b, err := parse(before)
	if err != nil {
		return nil, nil, err
	}
	a, err := parse(after)
	if err != nil {
		return nil, nil, fmt.Errorf("Beacon's edit would not parse (%v)", err)
	}
	return b, a, nil
}

// normalizeValue makes TOML and JSON decodings comparable (go-toml decodes integers as int64).
func normalizeValue(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var out any
	_ = decoder.Decode(&out)
	return out
}

func setPath(doc map[string]any, path []string, key string, value any) {
	cur := doc
	for _, seg := range path {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[key] = value
}

func deletePath(doc map[string]any, path []string, key string, prune int) {
	chain := []map[string]any{doc}
	cur := doc
	for _, seg := range path {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		chain = append(chain, next)
		cur = next
	}
	delete(cur, key)
	for level := len(path); level > 0 && len(path)-level < prune; level-- {
		if len(chain[level]) != 0 {
			return
		}
		delete(chain[level-1], path[level-1])
	}
}

func dropBeaconInputs(doc map[string]any) {
	list, ok := doc["inputs"].([]any)
	if !ok {
		return
	}
	kept := []any{}
	for _, v := range list {
		if !isBeaconInput(v) {
			kept = append(kept, v)
		}
	}
	doc["inputs"] = kept
}

// applyCLI registers the server through the harness's own CLI (Claude Code), which also owns the
// locking of a file its running sessions rewrite. The file is backed up first and read back after.
func applyCLI(ctx context.Context, opts Options, it Item, st state, rec Record) (Record, error) {
	rec.Method = it.Target.cli + "-cli"
	backup, err := st.file.backup(opts.now())
	if err != nil {
		return Record{}, fmt.Errorf("could not back up %s: %w", it.Path, err)
	}
	rec.Backup = backup
	// Replacing through the CLI is remove-then-add. Keep the removed entry's exact text so a
	// failed add can put it back rather than leave the harness with no server at all.
	var removed string
	if st.exists {
		removed, _ = rawMember(st.file.text, it.Target.container, ServerName)
		if out, err := opts.run(ctx, it.cliPath, "mcp", "remove", ServerName, "--scope", "user"); err != nil {
			return Record{}, fmt.Errorf("`%s mcp remove` failed: %v: %s", it.Target.cli, err, strings.TrimSpace(string(out)))
		}
	}
	fail := func(err error) (Record, error) {
		if removed != "" {
			if restoreErr := restoreMember(it.Target, it.Path, removed); restoreErr != nil {
				return Record{}, fmt.Errorf("%w; putting back the previous %s entry also failed (%v); it is in the backup %s", err, ServerName, restoreErr, backup)
			}
			return Record{}, fmt.Errorf("%w; the previous %s entry was put back", err, ServerName)
		}
		return Record{}, err
	}
	args := []string{"mcp", "add", "--transport", "http", "--scope", "user", ServerName, opts.URL}
	if opts.TokenEnv != "" {
		args = append(args, "--header", "Authorization: Bearer ${"+opts.TokenEnv+"}")
	}
	if out, err := opts.run(ctx, it.cliPath, args...); err != nil {
		return fail(fmt.Errorf("`%s mcp add` failed: %v: %s", it.Target.cli, err, strings.TrimSpace(string(out))))
	}
	after, err := inspect(it.Target, it.Path)
	if err != nil {
		return fail(err)
	}
	want := normalizeJSON(it.Target.entry(opts.URL, opts.TokenEnv))
	if !after.exists || !reflect.DeepEqual(normalizeJSON(after.entry), want) {
		return fail(fmt.Errorf("`%s mcp add` ran, but %s does not hold the expected %s entry", it.Target.cli, it.Path, ServerName))
	}
	return rec, nil
}

// restoreMember puts an entry's original JSON text back under the server name, when the name is
// free, with the same parse-back check as every other write.
func restoreMember(t Target, path, raw string) error {
	st, err := inspect(t, path)
	if err != nil {
		return err
	}
	if st.exists {
		return fmt.Errorf("%s already holds a %s entry", path, ServerName)
	}
	updated, _, err := insertJSONMember(st.file.text, t.container, ServerName, rawJSON(raw))
	if err != nil {
		return err
	}
	b, a, err := parseBoth(t, st.file.text, updated)
	if err != nil {
		return err
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	setPath(b, t.container, ServerName, value)
	if !reflect.DeepEqual(normalizeValue(b), normalizeValue(a)) {
		return errors.New("the restore would have changed more than the entry")
	}
	return st.file.write(updated)
}

// Disconnect removes the entries Beacon wrote for the targets, and nothing else.
func Disconnect(ctx context.Context, opts Options, targets []Target) ([]Item, error) {
	m, err := loadManifest(opts.Home)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, t := range targets {
		if !t.Automatic {
			continue
		}
		path, err := t.path(opts.Home)
		if err != nil {
			continue
		}
		it := Item{Target: t, Path: path}
		// Records are matched by harness and file. A harness whose config moved (CODEX_HOME,
		// CLAUDE_CONFIG_DIR) is still cleaned up at the path Beacon wrote.
		recs := recordsFor(m, t.Name)
		if len(recs) == 0 {
			it.Action, it.Detail = ActionAbsent, "nothing written by Beacon"
			items = append(items, it)
			continue
		}
		for _, rec := range recs {
			it := Item{Target: t, Path: rec.Path, Auth: rec.Auth, Method: rec.Method}
			action, detail, err := disconnectOne(ctx, opts, t, rec)
			it.Action, it.Detail, it.Err = action, detail, err
			if err == nil {
				m.drop(rec.Harness, rec.Path)
				if saveErr := m.save(opts.Home); saveErr != nil {
					it.Err = saveErr
				}
			}
			items = append(items, it)
		}
	}
	return items, nil
}

func recordsFor(m *manifest, harness string) []Record {
	var out []Record
	for _, r := range m.Records {
		if r.Harness == harness {
			out = append(out, r)
		}
	}
	return out
}

func disconnectOne(ctx context.Context, opts Options, t Target, rec Record) (Action, string, error) {
	st, err := inspect(t, rec.Path)
	if err != nil {
		return ActionSkip, "", err
	}
	if !st.exists {
		return ActionAbsent, "the entry is already gone", nil
	}
	if !sameURL(st.url, rec.URL) {
		return ActionAbsent, fmt.Sprintf("the %s entry now points at %s, not the URL Beacon wrote; left alone", ServerName, st.url), nil
	}
	if rec.Method == "claude-cli" {
		cliPath, err := opts.lookPath("claude")
		if err == nil {
			if out, err := opts.run(ctx, cliPath, "mcp", "remove", ServerName, "--scope", "user"); err != nil {
				return ActionSkip, "", fmt.Errorf("`claude mcp remove` failed: %v: %s", err, strings.TrimSpace(string(out)))
			}
			after, err := inspect(t, rec.Path)
			if err != nil {
				return ActionSkip, "", err
			}
			if after.exists {
				return ActionSkip, "", fmt.Errorf("`claude mcp remove` ran, but %s still holds %s", rec.Path, ServerName)
			}
			return ActionRemove, "removed with `claude mcp remove`", nil
		}
		// Without the CLI, fall through to editing the file directly.
	}
	cf := st.file
	// Nothing has touched the file since Beacon wrote it: put back the exact bytes.
	if rec.AfterSHA256 != "" && digest(cf.text) == rec.AfterSHA256 {
		if rec.CreatedFile {
			if err := cf.remove(); err != nil {
				return ActionSkip, "", err
			}
			return ActionRemove, "removed the file Beacon created", nil
		}
		if rec.Backup == "" && rec.BeforeSHA256 == digest("") {
			if err := cf.write(""); err != nil {
				return ActionSkip, "", err
			}
			return ActionRemove, "restored the file as it was before connect", nil
		}
		if rec.Backup != "" {
			if data, err := os.ReadFile(rec.Backup); err == nil && digest(string(data)) == rec.BeforeSHA256 {
				if err := cf.write(string(data)); err != nil {
					return ActionSkip, "", err
				}
				return ActionRemove, "restored the file as it was before connect", nil
			}
		}
	}
	updated, err := editForDisconnect(t, cf.text, rec)
	if err != nil {
		return ActionSkip, "", fmt.Errorf("could not edit %s: %w", rec.Path, err)
	}
	if err := verifyDisconnect(t, cf.text, updated, rec); err != nil {
		return ActionSkip, "", fmt.Errorf("%s: %w; the file was left unchanged", rec.Path, err)
	}
	if rec.CreatedFile && onlySkeleton(t, updated) {
		if err := cf.remove(); err != nil {
			return ActionSkip, "", err
		}
		return ActionRemove, "removed the file Beacon created", nil
	}
	if _, err := cf.backup(opts.now()); err != nil {
		return ActionSkip, "", fmt.Errorf("could not back up %s: %w", rec.Path, err)
	}
	if err := cf.write(updated); err != nil {
		return ActionSkip, "", err
	}
	return ActionRemove, "removed Beacon's entry", nil
}

func editForDisconnect(t Target, text string, rec Record) (string, error) {
	if t.format == formatTOML {
		stripped, found := stripTOMLTable(text, t.container[0], ServerName)
		if !found {
			return "", errors.New("the " + ServerName + " entry is not a [" + t.container[0] + "." + ServerName + "] table; remove it by hand")
		}
		return endTOMLWithNewline(stripped, tomlLineEnding(text)), nil
	}
	updated, err := removeJSONMember(text, t.container, ServerName, rec.CreatedParents)
	if err != nil {
		return "", err
	}
	if t.vscodeInputs && rec.Auth == AuthTokenEnv {
		if updated, err = removeJSONElements(updated, nil, "inputs", isBeaconInput, rec.CreatedInputs); err != nil {
			return "", err
		}
	}
	return updated, nil
}

// onlySkeleton reports whether a config Beacon created holds nothing but what Beacon put there.
func onlySkeleton(t Target, text string) bool {
	parse := decodeJSONC
	if t.format == formatTOML {
		parse = parseTOML
	}
	doc, err := parse(text)
	if err != nil {
		return false
	}
	for _, f := range t.skeleton {
		if reflect.DeepEqual(doc[f.Key], normalizeJSON(f.Value)) {
			delete(doc, f.Key)
		}
	}
	for _, key := range t.container[:1] {
		if m, ok := doc[key].(map[string]any); ok && len(m) == 0 {
			delete(doc, key)
		}
	}
	return len(doc) == 0
}

// Status describes what a harness has configured for the server name.
type Status struct {
	Target     Target
	Path       string
	Configured bool
	URL        string
	Auth       AuthMode
	ByBeacon   bool
	Detail     string
}

// Inspect reports the current state of each target without changing anything.
func Inspect(opts Options, targets []Target) ([]Status, error) {
	m, err := loadManifest(opts.Home)
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, t := range targets {
		s := Status{Target: t}
		if !t.Automatic {
			s.Auth, s.Detail = AuthManual, "configured by hand; Beacon does not read this runtime's MCP config"
			out = append(out, s)
			continue
		}
		path, err := t.path(opts.Home)
		if err != nil {
			s.Detail = err.Error()
			out = append(out, s)
			continue
		}
		s.Path = path
		st, err := inspect(t, path)
		if err != nil {
			s.Detail = err.Error()
			out = append(out, s)
			continue
		}
		if st.exists {
			s.Configured, s.URL = true, st.url
			s.Auth = entryAuth(st.entry)
			rec, ok := m.find(t.Name, path)
			s.ByBeacon = ok && sameURL(rec.URL, st.url)
		}
		out = append(out, s)
	}
	return out, nil
}

// entryAuth infers the auth mode of an entry from its shape. A header value is inspected only
// for a variable reference and is never returned or printed.
func entryAuth(entry map[string]any) AuthMode {
	if _, ok := entry["bearer_token_env_var"]; ok {
		return AuthTokenEnv
	}
	for _, key := range []string{"headers", "http_headers"} {
		headers, ok := entry[key].(map[string]any)
		if !ok || len(headers) == 0 {
			continue
		}
		for _, v := range headers {
			s, _ := v.(string)
			if strings.Contains(s, "${") || strings.Contains(s, "{env:") {
				return AuthTokenEnv
			}
		}
		return AuthToken
	}
	return AuthOAuth
}
