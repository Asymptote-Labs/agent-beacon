package gitlink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	hookBlockStart = "# >>> beacon git hooks (do not edit)"
	hookBlockEnd   = "# <<< beacon git hooks"
	// HookScriptPrefix names Beacon's standalone hook scripts: beacon-post-commit next to
	// post-commit. The hook git runs calls the script through a marked block, so a repository's own
	// hook keeps working and Beacon's part can be removed without touching the rest.
	HookScriptPrefix = "beacon-"
	// DisableEnv, set to 0, turns every installed Beacon git hook into a no-op for one command:
	// BEACON_GIT_HOOKS=0 git commit ...
	DisableEnv = "BEACON_GIT_HOOKS"
)

// ManagedHooks are the git hooks Beacon installs. post-commit links a new commit; post-rewrite
// tidies the note on a commit `git commit --amend` or `git rebase` rewrote, after git has copied
// the old commit's note across (see NormalizeNote).
var ManagedHooks = []string{"post-commit", "post-rewrite"}

// ShareHook is the hook `beacon git setup --share-notes` adds. It is off by default because it is
// the one Beacon hook that reaches the network: during a push the person started, it pushes
// refs/notes/beacon to the same remote.
const ShareHook = "pre-push"

// allHooks is every hook Beacon may have installed, which is what remove and status look at.
var allHooks = append(append([]string{}, ManagedHooks...), ShareHook)

// ErrHooksPathSet is returned by InstallHooks when core.hooksPath points git somewhere other than
// the repository's own hooks directory -- usually a hook manager's tracked directory -- and the
// caller did not ask to write there.
var ErrHooksPathSet = errors.New("core.hooksPath is set")

// ErrForeignHook is returned when an existing hook is not a shell script, so a shell block cannot
// be added to it safely.
var ErrForeignHook = errors.New("existing hook is not a shell script")

// HooksLocation is where git looks for this repository's hooks.
type HooksLocation struct {
	Dir string `json:"dir"`
	// HooksPath is core.hooksPath when it is set.
	HooksPath string `json:"hooks_path,omitempty"`
}

// ResolveHooks returns the directory git runs this repository's hooks from. It honors
// core.hooksPath, and for a linked worktree it is the shared directory in the common git dir.
func ResolveHooks(ctx context.Context, repo Repo) (HooksLocation, error) {
	dir, err := repo.Git.Run(ctx, nil, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return HooksLocation{}, err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo.Root, dir)
	}
	loc := HooksLocation{Dir: filepath.Clean(dir)}
	if value, err := repo.Git.Run(ctx, nil, "config", "--get", "core.hooksPath"); err == nil {
		loc.HooksPath = strings.TrimSpace(value)
	}
	return loc, nil
}

// InstallOptions configures InstallHooks.
type InstallOptions struct {
	// BeaconPath is the absolute path of the beacon binary the hook scripts call first. They fall
	// back to beacon on PATH when it is missing, which is what a package upgrade that moves the
	// binary leaves behind.
	BeaconPath string
	// AllowHooksPath installs into core.hooksPath's directory when it is set.
	AllowHooksPath bool
	// ShareNotes, when set, turns sharing on (true) or off (false): the pre-push hook and a fetch
	// refspec on every remote. Nil leaves sharing as it is, so re-running setup to repair an
	// install does not change it.
	ShareNotes *bool
}

// HookReport is what installing or removing did to one hook.
type HookReport struct {
	Hook   string `json:"hook"`
	Path   string `json:"path"`
	Script string `json:"script"`
	// Action is "created" (no hook existed), "added" (Beacon's block was added to an existing
	// hook), "updated", "unchanged", "removed", or "absent".
	Action string `json:"action"`
}

// InstallResult is the outcome of InstallHooks.
type InstallResult struct {
	Hooks         HooksLocation `json:"hooks"`
	Reports       []HookReport  `json:"reports"`
	RewriteRefSet bool          `json:"rewrite_ref_set"`
	// FetchAdded and FetchRemoved are the remotes whose fetch refspec setup changed.
	FetchAdded   []string `json:"fetch_added,omitempty"`
	FetchRemoved []string `json:"fetch_removed,omitempty"`
}

// InstallHooks installs Beacon's hooks. It is idempotent: a second run rewrites the scripts and
// leaves the hooks unchanged.
func InstallHooks(ctx context.Context, repo Repo, opts InstallOptions) (InstallResult, error) {
	loc, err := ResolveHooks(ctx, repo)
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{Hooks: loc}
	if loc.HooksPath != "" && !opts.AllowHooksPath {
		return result, fmt.Errorf("%w to %s", ErrHooksPathSet, loc.HooksPath)
	}
	if err := os.MkdirAll(loc.Dir, 0o755); err != nil {
		return result, err
	}
	hooks := append([]string{}, ManagedHooks...)
	if opts.ShareNotes != nil && *opts.ShareNotes {
		hooks = append(hooks, ShareHook)
	} else if opts.ShareNotes == nil && hookInstalled(loc.Dir, ShareHook) {
		hooks = append(hooks, ShareHook) // refresh the script along with the others
	}
	// Check every hook before writing any, so a refusal leaves the repository as it was.
	for _, hook := range hooks {
		if err := checkHookEditable(filepath.Join(loc.Dir, hook)); err != nil {
			return result, fmt.Errorf("%s: %w", filepath.Join(loc.Dir, hook), err)
		}
	}
	for _, hook := range hooks {
		report, err := installHook(loc.Dir, hook, opts.BeaconPath)
		if err != nil {
			return result, err
		}
		result.Reports = append(result.Reports, report)
	}
	set, err := ensureRewriteRef(ctx, repo)
	if err != nil {
		return result, err
	}
	result.RewriteRefSet = set
	if opts.ShareNotes == nil {
		return result, nil
	}
	if *opts.ShareNotes {
		remotes, err := Remotes(ctx, repo)
		if err != nil {
			return result, err
		}
		result.FetchAdded, err = ConfigureFetch(ctx, repo, remotes)
		return result, err
	}
	report, err := removeHook(loc.Dir, ShareHook)
	if err != nil {
		return result, err
	}
	if report.Action == "removed" {
		result.Reports = append(result.Reports, report)
	}
	result.FetchRemoved, err = UnconfigureFetch(ctx, repo)
	return result, err
}

func hookInstalled(dir, hook string) bool {
	data, err := os.ReadFile(filepath.Join(dir, hook))
	return err == nil && strings.Contains(string(data), hookBlockStart) && fileExists(filepath.Join(dir, HookScriptPrefix+hook))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func installHook(dir, hook, beaconPath string) (HookReport, error) {
	scriptName := HookScriptPrefix + hook
	report := HookReport{Hook: hook, Path: filepath.Join(dir, hook), Script: filepath.Join(dir, scriptName)}
	if err := writeExecutable(report.Script, hookScript(hook, beaconPath)); err != nil {
		return report, err
	}
	existing, err := os.ReadFile(report.Path)
	if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	block := hookBlock(scriptName)
	var next string
	switch {
	case os.IsNotExist(err) || strings.TrimSpace(string(existing)) == "":
		report.Action = "created"
		next = "#!/bin/sh\n" + block
	case strings.Contains(string(existing), hookBlockStart):
		report.Action = "updated"
		next = insertBlock(removeBlock(string(existing)), block)
		if next == string(existing) {
			report.Action = "unchanged"
		}
	default:
		report.Action = "added"
		next = insertBlock(string(existing), block)
	}
	if report.Action != "unchanged" {
		if err := writeExecutable(report.Path, next); err != nil {
			return report, err
		}
	} else if err := os.Chmod(report.Path, 0o755); err != nil {
		return report, err
	}
	return report, nil
}

// insertBlock puts Beacon's block directly after the shebang. Running first matters: a hook that
// ends in `exit 0` or `exec` would never reach a block appended after it. Beacon's part cannot
// fail or stop the rest, so going first changes nothing the existing hook does.
func insertBlock(script, block string) string {
	if !strings.HasPrefix(script, "#!") {
		return "#!/bin/sh\n" + block + script
	}
	shebang, rest, _ := strings.Cut(script, "\n")
	return shebang + "\n" + block + rest
}

func removeBlock(script string) string {
	start := strings.Index(script, hookBlockStart)
	if start < 0 {
		return script
	}
	end := strings.Index(script[start:], hookBlockEnd)
	if end < 0 {
		return script
	}
	end = start + end + len(hookBlockEnd)
	if end < len(script) && script[end] == '\n' {
		end++
	}
	return script[:start] + script[end:]
}

var shellShebang = regexp.MustCompile(`^#!\s*(/usr/bin/env\s+(-S\s+)?)?(\S*/)?(sh|bash|dash|zsh|ksh|ash)(\s|$)`)

func checkHookEditable(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(data)
	if strings.TrimSpace(text) == "" || strings.Contains(text, hookBlockStart) {
		return nil
	}
	first, _, _ := strings.Cut(text, "\n")
	if !strings.HasPrefix(first, "#!") {
		// No shebang: git runs it with sh.
		return nil
	}
	if !shellShebang.MatchString(first) {
		return fmt.Errorf("%w (%s)", ErrForeignHook, strings.TrimSpace(first))
	}
	return nil
}

func hookBlock(scriptName string) string {
	return hookBlockStart + "\n" +
		`if [ -x "$(git rev-parse --git-path hooks)/` + scriptName + `" ]; then` + "\n" +
		`  "$(git rev-parse --git-path hooks)/` + scriptName + `" "$@"` + "\n" +
		"fi\n" +
		hookBlockEnd + "\n"
}

// HookCall is the line to put in a hook Beacon cannot edit (a hook manager's, or one written in
// another language) so it links commits too.
func HookCall(hook string) string {
	return "beacon git hook " + hook + " || true"
}

var hookPurpose = map[string]string{
	"post-commit":  "links the new commit to the agent sessions that wrote it.",
	"post-rewrite": "tidies the Beacon note on commits an amend or rebase rewrote.",
	"pre-push":     "shares refs/notes/beacon with the remote being pushed to.",
}

func hookScript(hook, beaconPath string) string {
	// post-rewrite reads the rewritten commits from stdin; the others get nothing to read.
	// pre-push needs the remote git passes, and keeps stderr so a failure to share says so.
	stdin, args, output := " </dev/null", "", " >/dev/null 2>&1"
	switch hook {
	case "post-rewrite":
		stdin, args = "", ` "$@"`
	case ShareHook:
		args, output = ` "$@"`, " >/dev/null"
	}
	return `#!/bin/sh
# Beacon ` + hook + ` hook: ` + hookPurpose[hook] + `
# Installed by: beacon git setup    Removed by: beacon git remove
# ` + hookScope(hook) + `
[ "${` + DisableEnv + `:-1}" = "0" ] && exit 0
beacon_bin=` + shellQuote(beaconPath) + `
[ -n "$beacon_bin" ] && [ -x "$beacon_bin" ] || beacon_bin="$(command -v beacon 2>/dev/null)" || exit 0
"$beacon_bin" git hook ` + hook + args + stdin + output + `
exit 0
`
}

func hookScope(hook string) string {
	if hook == ShareHook {
		return "Pushes only Beacon's notes ref, only to this push's remote, and never fails the push."
	}
	return "Reads the local runtime log and writes a git note; never touches the network or fails the commit."
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeExecutable(path, content string) error {
	tmp := path + ".beacon-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveResult is the outcome of RemoveHooks.
type RemoveResult struct {
	Hooks             HooksLocation `json:"hooks"`
	Reports           []HookReport  `json:"reports"`
	RewriteRefRemoved bool          `json:"rewrite_ref_removed"`
	FetchRemoved      []string      `json:"fetch_removed,omitempty"`
}

func removeHook(dir, hook string) (HookReport, error) {
	report := HookReport{Hook: hook, Path: filepath.Join(dir, hook), Script: filepath.Join(dir, HookScriptPrefix+hook), Action: "absent"}
	if err := os.Remove(report.Script); err == nil {
		report.Action = "removed"
	} else if !os.IsNotExist(err) {
		return report, err
	}
	data, err := os.ReadFile(report.Path)
	if err == nil && strings.Contains(string(data), hookBlockStart) {
		rest := removeBlock(string(data))
		if onlyShebang(rest) {
			err = os.Remove(report.Path)
		} else {
			err = writeExecutable(report.Path, rest)
		}
		if err != nil {
			return report, err
		}
		report.Action = "removed"
	} else if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	return report, nil
}

// RemoveHooks removes Beacon's scripts and blocks. A hook left with nothing but a shebang is
// deleted; anything else in it stays. The notes themselves are never removed: they are history.
func RemoveHooks(ctx context.Context, repo Repo) (RemoveResult, error) {
	loc, err := ResolveHooks(ctx, repo)
	if err != nil {
		return RemoveResult{}, err
	}
	result := RemoveResult{Hooks: loc}
	for _, hook := range allHooks {
		report, err := removeHook(loc.Dir, hook)
		if err != nil {
			return result, err
		}
		if hook == ShareHook && report.Action == "absent" {
			continue // never installed; not worth a line
		}
		result.Reports = append(result.Reports, report)
	}
	if result.FetchRemoved, err = UnconfigureFetch(ctx, repo); err != nil {
		return result, err
	}
	removed, err := removeRewriteRef(ctx, repo)
	if err != nil {
		return result, err
	}
	result.RewriteRefRemoved = removed
	return result, nil
}

func onlyShebang(script string) bool {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#!") {
			return false
		}
	}
	return true
}

// HookState is whether one hook is wired to Beacon.
type HookState struct {
	Hook string `json:"hook"`
	// Installed is true when the hook calls Beacon's script and the script exists.
	Installed    bool `json:"installed"`
	BlockPresent bool `json:"block_present"`
	ScriptExists bool `json:"script_exists"`
}

// Status is the state of Beacon's git integration in one repository.
type Status struct {
	Hooks      HooksLocation `json:"hooks"`
	States     []HookState   `json:"states"`
	RewriteRef bool          `json:"rewrite_ref"`
	// Sharing is true when the pre-push hook is installed; FetchRemotes are the remotes whose
	// fetches bring their links along.
	Sharing      bool     `json:"sharing"`
	FetchRemotes []string `json:"fetch_remotes,omitempty"`
}

// Installed is true when every managed hook is installed. Sharing is optional and not counted.
func (s Status) Installed() bool {
	n := 0
	for _, st := range s.States {
		if st.Hook == ShareHook {
			continue
		}
		if !st.Installed {
			return false
		}
		n++
	}
	return n == len(ManagedHooks)
}

// HookStatus reports Beacon's git integration in repo.
func HookStatus(ctx context.Context, repo Repo) (Status, error) {
	loc, err := ResolveHooks(ctx, repo)
	if err != nil {
		return Status{}, err
	}
	status := Status{Hooks: loc}
	for _, hook := range allHooks {
		st := HookState{Hook: hook}
		if data, err := os.ReadFile(filepath.Join(loc.Dir, hook)); err == nil {
			st.BlockPresent = strings.Contains(string(data), hookBlockStart)
		}
		if _, err := os.Stat(filepath.Join(loc.Dir, HookScriptPrefix+hook)); err == nil {
			st.ScriptExists = true
		}
		st.Installed = st.BlockPresent && st.ScriptExists
		if hook == ShareHook {
			status.Sharing = st.Installed
			if !st.BlockPresent && !st.ScriptExists {
				continue // not installed and not half-installed: nothing to report
			}
		}
		status.States = append(status.States, st)
	}
	if remotes, err := Remotes(ctx, repo); err == nil {
		for _, remote := range remotes {
			if has, err := hasFetchRefspec(ctx, repo, remote); err == nil && has {
				status.FetchRemotes = append(status.FetchRemotes, remote)
			}
		}
	}
	status.RewriteRef, err = hasRewriteRef(ctx, repo)
	return status, err
}

// ensureRewriteRef adds NotesRef to notes.rewriteRef, so `git commit --amend` and `git rebase`
// carry a commit's links to the commit that replaces it.
func ensureRewriteRef(ctx context.Context, repo Repo) (bool, error) {
	if has, err := hasRewriteRef(ctx, repo); err != nil || has {
		return false, err
	}
	if _, err := repo.Git.Run(ctx, nil, "config", "--local", "--add", "notes.rewriteRef", NotesRef); err != nil {
		return false, err
	}
	return true, nil
}

func hasRewriteRef(ctx context.Context, repo Repo) (bool, error) {
	out, err := repo.Git.Run(ctx, nil, "config", "--local", "--get-all", "notes.rewriteRef")
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) {
			return false, nil // exit 1: no value
		}
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == NotesRef {
			return true, nil
		}
	}
	return false, nil
}

func removeRewriteRef(ctx context.Context, repo Repo) (bool, error) {
	has, err := hasRewriteRef(ctx, repo)
	if err != nil || !has {
		return false, err
	}
	pattern := "^" + regexp.QuoteMeta(NotesRef) + "$"
	if _, err := repo.Git.Run(ctx, nil, "config", "--local", "--unset-all", "notes.rewriteRef", pattern); err != nil {
		return false, err
	}
	return true, nil
}

// ReplayInProgress reports whether git is replaying existing commits -- a rebase, cherry-pick or
// revert -- rather than recording new work. Their commits' links, when they have any, travel with
// notes.rewriteRef; attributing them afresh would credit whoever is rebasing today.
func ReplayInProgress(ctx context.Context, repo Repo) bool {
	for _, name := range []string{"rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"} {
		path, err := repo.Git.Run(ctx, nil, "rev-parse", "--git-path", name)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo.Root, path)
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}
