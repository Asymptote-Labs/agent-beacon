package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/osuser"
	"github.com/spf13/cobra"
)

var endpointHooksRepairInstalledCmd = &cobra.Command{
	Use:          "repair-installed",
	Short:        "Refresh installed user hooks",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runEndpointHooksRepairInstalled,
}

type consoleUserInfo struct {
	Username string
	HomeDir  string
}

type endpointHookRepairResult struct {
	User           string   `json:"user,omitempty"`
	HomeDir        string   `json:"home_dir,omitempty"`
	RuntimeLogPath string   `json:"runtime_log_path,omitempty"`
	Targets        []string `json:"targets,omitempty"`
	SkippedReason  string   `json:"skipped_reason,omitempty"`
}

var (
	activeConsoleUser = defaultActiveConsoleUser
	// lookupConsoleUser is a seam so the SUDO_USER-before-logind ordering can be tested without
	// depending on which accounts happen to exist on the machine running the tests -- as root in
	// a container, the obvious test skips, which left the ordering unverified.
	lookupConsoleUser   = resolveConsoleUser
	runHookRepairAsUser = defaultRunHookRepairAsUser
)

func runEndpointHooksRepairInstalled(cmd *cobra.Command, args []string) error {
	result, err := repairInstalledEndpointHooks()
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
	return err
}

func repairInstalledEndpointHooks() (endpointHookRepairResult, error) {
	info, ok, err := activeConsoleUser()
	if err != nil {
		return endpointHookRepairResult{}, err
	}
	if !ok {
		return endpointHookRepairResult{SkippedReason: "no_active_console_user"}, nil
	}

	logPath := endpointOpts.logPath
	if logPath == "" {
		cfg := loadConfigForMode(false, "")
		logPath = cfg.LogPath
	}
	if logPath == "" {
		logPath = writer.DefaultPath(false)
	}

	targets, err := repairHookTargetsForUser(info, logPath, nil)
	if err != nil {
		return endpointHookRepairResult{}, err
	}
	return endpointHookRepairResult{
		User:           info.Username,
		HomeDir:        info.HomeDir,
		RuntimeLogPath: logPath,
		Targets:        targets,
	}, nil
}

func repairHookTargetsForUser(info consoleUserInfo, logPath string, requestedTargets []string) ([]string, error) {
	targetSet := map[string]bool{}
	if requestedTargets != nil {
		for _, target := range requestedTargets {
			targetSet[target] = true
		}
	} else if strings.TrimSpace(endpointOpts.hookHarnesses) != "" {
		targets, err := canonicalHookTargets(splitCSV(endpointOpts.hookHarnesses))
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			targetSet[target] = true
		}
	}
	installed, err := installedHookTargetsForUser(info.HomeDir, logPath)
	if err != nil {
		return nil, err
	}
	for _, target := range installed {
		targetSet[target] = true
	}
	targets := orderedRepairTargets(targetSet)
	if len(targets) == 0 {
		return nil, nil
	}
	args := []string{"endpoint", "hooks", "install", "--harness", strings.Join(targets, ","), "--level", endpointOpts.hookLevel, "--log-path", logPath}
	if err := runHookRepairAsUser(info, args...); err != nil {
		return targets, err
	}
	return targets, nil
}

func orderedRepairTargets(targetSet map[string]bool) []string {
	targets := []string{}
	for _, target := range repairTargetOrder() {
		if targetSet[target] {
			targets = append(targets, target)
		}
	}
	return targets
}

func installedHookTargetsForUser(homeDir, logPath string) ([]string, error) {
	candidates, err := canonicalHookTargets(repairTargetOrder())
	if err != nil {
		return nil, err
	}
	return withUserHome(homeDir, func() ([]string, error) {
		cfg := endpointconfig.Default(false, logPath)
		statuses := hookStatusesWithConfig(candidates, cfg)
		targets := []string{}
		for _, name := range candidates {
			if status, ok := statuses[name]; ok && status.Installed {
				targets = append(targets, name)
			}
		}
		return targets, nil
	})
}

func repairTargetOrder() []string {
	return []string{"claude", "codex", "cursor", "vscode", "factory", "opencode", "cline", "pi", "grok", "qwen", "hermes", "devin-cli", "devin-desktop", "antigravity"}
}

// withUserHome runs fn as if it were executing inside another user's profile.
//
// Setting HOME alone was enough while every platform was POSIX. It is not on Windows:
// os.UserHomeDir reads USERPROFILE there and ignores HOME entirely, so a system-mode repair would
// resolve every runtime config path inside the *installing* account's profile and write hooks that
// the person at the keyboard never runs -- the same silent shape as the Linux SUDO_USER bug this
// whole code path exists to fix.
//
// APPDATA and LOCALAPPDATA come with it because several runtimes resolve their config from those
// rather than from the profile root, and leaving them pointed at the installing account would move
// the same bug one directory over. They are derived from the profile rather than read, since the
// values in this process describe the wrong user by definition.
func withUserHome[T any](homeDir string, fn func() (T, error)) (T, error) {
	restore := make([]func(), 0, 4)
	for _, entry := range profileEnvironment(homeDir) {
		key, value := entry[0], entry[1]
		previous, existed := os.LookupEnv(key)
		restore = append(restore, func() {
			if existed {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
		_ = os.Setenv(key, value)
	}
	defer func() {
		for _, undo := range restore {
			undo()
		}
	}()
	return fn()
}

// profileEnvironment lists the variables that make path resolution land in a given user's profile.
//
// Shared by the in-process path and the child-process one so the two cannot disagree about which
// profile they are targeting.
func profileEnvironment(homeDir string) [][2]string {
	if runtime.GOOS != "windows" {
		return [][2]string{{"HOME", homeDir}}
	}
	return [][2]string{
		// HOME is still set: it is what Git and other POSIX-shaped tools on Windows read, and the
		// hook commands Beacon writes are executed by Git Bash there.
		{"HOME", homeDir},
		{"USERPROFILE", homeDir},
		{"APPDATA", filepath.Join(homeDir, "AppData", "Roaming")},
		{"LOCALAPPDATA", filepath.Join(homeDir, "AppData", "Local")},
	}
}

// defaultActiveConsoleUser resolves the human whose runtime configuration a system install should
// reach.
//
// A system endpoint runs as root, and `endpoint install` configures the *installing* user's
// Claude Code and Codex settings -- which for a package install is root, not the person at the
// keyboard. Everything the operator actually wants captured therefore depends on resolving that
// person, which is what this does. When it cannot, callers report "no active console user" and
// skip rather than pretending to have configured someone.
func defaultActiveConsoleUser() (consoleUserInfo, bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return darwinActiveConsoleUser()
	case "linux":
		return linuxActiveConsoleUser()
	case "windows":
		return windowsActiveConsoleUser()
	}
	return consoleUserInfo{}, false, nil
}

// linuxActiveConsoleUser resolves the console user from sudo's environment, then from logind.
//
// SUDO_USER comes first because it is the exact question being asked -- "who invoked this install"
// -- and it is set for every realistic path (`sudo apt install ./beacon.deb`, `sudo dpkg -i`,
// `sudo rpm -U`, `sudo ./install-endpoint.sh`). logind is the fallback for the cases sudo cannot
// answer: an unattended fleet run, a root shell, or a re-run from a systemd unit. Neither is
// guessed at: an unresolvable user is reported as absent.
func linuxActiveConsoleUser() (consoleUserInfo, bool, error) {
	if u := strings.TrimSpace(os.Getenv("SUDO_USER")); u != "" {
		return lookupConsoleUser(u)
	}
	// The session table is used only to enumerate ids. Every other column is read back through
	// `show-session`, which reports `Key=Value` lines -- unambiguous, self-labeling, and free of the
	// table's placeholders.
	//
	// Parsing the table directly is a trap, and two separate ones. Its unseated rows print `-` in
	// the seat column rather than leaving it blank, so "non-empty means seated" classifies every ssh
	// session as seated. And `--value` returns properties in alphabetical order, not the order
	// requested, so reading two values positionally silently swaps them. Both were verified against
	// systemd 255 rather than assumed.
	out, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output()
	if err != nil {
		// No logind, or it refused. Not an error worth failing an install over: this is a
		// best-effort identification, and the caller already handles "unknown".
		return consoleUserInfo{}, false, nil
	}
	var sessions []logindSession
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if s, ok := describeLogindSession(fields[0]); ok {
			sessions = append(sessions, s)
		}
	}
	// Best answer first: someone at the machine in the foreground, then anyone at the machine, then
	// a remote login. Ranked rather than filtered -- a remote session is still a real human whose
	// agent runs on this host, so it is the right answer when it is the only one.
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].rank() > sessions[j].rank() })
	for _, sess := range sessions {
		if info, ok, err := lookupConsoleUser(sess.user); ok || err != nil {
			return info, ok, err
		}
	}
	return consoleUserInfo{}, false, nil
}

// logindSession is what one session says about itself.
type logindSession struct {
	user   string
	seated bool
	// foreground is logind's "active" state: the session currently in front on its seat. A logged-in
	// user who is not in front reports "online" instead, which is still a real login -- requiring
	// "active" alone would reject a seated graphical session in favour of an ssh one, since that is
	// exactly what systemd 255 reports for each.
	foreground bool
}

func (s logindSession) rank() int {
	score := 0
	if s.seated {
		score += 2
	}
	if s.foreground {
		score++
	}
	return score
}

// describeLogindSession reads one session's properties, skipping anything that is not a live login.
func describeLogindSession(id string) (logindSession, bool) {
	out, err := exec.Command("loginctl", "show-session", id, "-p", "Name", "-p", "State", "-p", "Seat").Output()
	if err != nil {
		return logindSession{}, false
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, found := strings.Cut(strings.TrimSpace(line), "="); found {
			props[k] = v
		}
	}
	// closing and opening sessions are mid-transition and are not somebody to configure.
	state := props["State"]
	if state != "active" && state != "online" {
		return logindSession{}, false
	}
	if props["Name"] == "" {
		return logindSession{}, false
	}
	return logindSession{
		user:       props["Name"],
		seated:     strings.TrimSpace(props["Seat"]) != "",
		foreground: state == "active",
	}, true
}

// resolveConsoleUser validates a username and resolves its home directory.
//
// root is rejected: a system install has already configured root, and treating it as the console
// user would report success for configuring nobody. A user without a real home is rejected for
// the same reason -- there is nowhere for a settings file to go.
//
// The lookup goes through osuser rather than os/user directly, and that is the whole difference
// between working and not working on a directory-backed fleet. Beacon is built CGO_ENABLED=0, so
// os/user reads /etc/passwd and never consults NSS; on a host whose accounts come from OpenLDAP or
// SSSD, every developer is invisible to a by-name lookup. Both callers reach this function -- the
// SUDO_USER path and the logind path -- so this is the single point where that was decided, and
// the logind case made it especially wasteful: logind is NSS-aware and hands over the correct
// username, which this function then discarded by re-resolving it somewhere it could not be found.
func resolveConsoleUser(username string) (consoleUserInfo, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || username == "root" {
		return consoleUserInfo{}, false, nil
	}
	u, err := osuser.Lookup(username)
	if err != nil {
		return consoleUserInfo{}, false, nil
	}
	if u.HomeDir == "" || u.HomeDir == "/" || u.HomeDir == "/nonexistent" {
		return consoleUserInfo{}, false, nil
	}
	if _, err := os.Stat(u.HomeDir); err != nil {
		return consoleUserInfo{}, false, nil
	}
	// The resolved spelling rather than the requested one: a directory service may answer with the
	// account's canonical name, and that is what the settings file and its ownership should use.
	return consoleUserInfo{Username: u.Username, HomeDir: u.HomeDir}, true, nil
}

func darwinActiveConsoleUser() (consoleUserInfo, bool, error) {
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil {
		return consoleUserInfo{}, false, err
	}
	username := strings.TrimSpace(string(out))
	if username == "" || username == "root" || username == "loginwindow" {
		return consoleUserInfo{}, false, nil
	}
	homeOut, err := exec.Command("dscl", ".", "-read", "/Users/"+username, "NFSHomeDirectory").Output()
	if err != nil {
		return consoleUserInfo{}, false, err
	}
	home := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(homeOut)), "NFSHomeDirectory:"))
	if home == "" {
		return consoleUserInfo{}, false, fmt.Errorf("could not resolve home directory for %s", username)
	}
	return consoleUserInfo{Username: username, HomeDir: home}, true, nil
}

func defaultRunHookRepairAsUser(info consoleUserInfo, args ...string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		return runHookRepairInProfile(ctx, bin, info, args...)
	}
	// runuser is the root-side tool for this and ships with util-linux, so it is present on a
	// minimal Debian or RPM system where sudo may not be installed at all -- and a package
	// postinstall runs as root, which is exactly runuser's precondition. sudo remains the path
	// everywhere else, including macOS.
	launcher, cmdArgs := "sudo", []string{"-u", info.Username}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if path, err := exec.LookPath("runuser"); err == nil {
			launcher, cmdArgs = path, []string{"-u", info.Username, "--"}
		}
	}
	cmdArgs = append(cmdArgs, "env", "HOME="+info.HomeDir, "USER="+info.Username,
		"LOGNAME="+info.Username, bin)
	cmdArgs = append(cmdArgs, args...)
	out, err := exec.CommandContext(ctx, launcher, cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("refresh hooks for %s: %s: %w", info.Username, strings.TrimSpace(string(out)), err)
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		fmt.Println(text)
	}
	return nil
}

// runHookRepairInProfile re-runs this binary against another user's profile on Windows.
//
// There is no sudo and no runuser there. The POSIX path uses them to *drop* privileges so the files it
// writes end up owned by the person whose profile they live in; Windows has no comparable one-liner,
// so this keeps the elevated identity and redirects the profile instead.
//
// That is a real difference, and it is acceptable for what these files are. A hook config written into
// C:\Users\Jane\.cursor\ inherits that directory's ACL, which grants Jane full control -- so she can
// read it, and her runtime can rewrite it, even though an administrator created it. What she does not
// get is ownership, which nothing in this path depends on.
//
// The honest alternative is CreateProcessAsUser with the token WTS already hands back for the console
// session, which would make the files hers outright. It is a good deal more code for a property no
// caller relies on, so it is not done here rather than done badly.
//
// Still a child process rather than an in-process call, matching POSIX: the repair reconfigures global
// state through env vars, and doing that inside the running process would leave a system install's own
// paths pointed at a user profile if anything after it failed.
func runHookRepairInProfile(ctx context.Context, bin string, info consoleUserInfo, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	env := os.Environ()
	for _, entry := range profileEnvironment(info.HomeDir) {
		env = append(env, entry[0]+"="+entry[1])
	}
	// Appended rather than replaced: a later assignment wins in Go's exec, so these override the
	// inherited values while everything else the child needs -- PATH, SystemRoot -- survives. Dropping
	// SystemRoot in particular makes Windows networking and crypto calls fail in ways that read as
	// unrelated bugs.
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("refresh hooks for %s: %s: %w", info.Username, strings.TrimSpace(string(out)), err)
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		fmt.Println(text)
	}
	return nil
}
