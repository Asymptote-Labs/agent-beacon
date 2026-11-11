// Package loggroup manages the group that may read a restricted system runtime log.
//
// In the restricted log access mode the system log is 0640 root:beacon-log: root writes it, members
// of the group read it, nobody else has access. Install creates the group when it is missing and
// puts nobody in it; an administrator adds the accounts that should read every user's sessions,
// such as a log shipper's service account. Uninstall leaves the group, since it may have members an
// administrator chose.
//
// Accounts and groups on both platforms can come from a directory service (SSSD, LDAP, AD, macOS
// Directory Services) that Go's CGO_ENABLED=0 os/user never consults, so lookups go through the
// system's own tools -- getent on Linux, dscl on macOS -- the way package osuser resolves accounts.
package loggroup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Name is the readers group.
const Name = asymptoteobserve.RuntimeLogReadersGroup

const commandTimeout = 10 * time.Second

// ErrUnsupported is returned where Beacon cannot manage groups.
var ErrUnsupported = errors.New("managing the log readers group is not supported on this platform")

// toolError is a system tool that failed. Stderr is what the tool itself printed, kept apart from
// the command line in Error(), so a caller that recognizes a tool's message by its text (net.exe's
// system error number, say) never matches an account name or path it was given.
type toolError struct {
	command string
	err     error
	Stderr  string
}

func (e *toolError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %v: %s", e.command, e.err, e.Stderr)
	}
	return fmt.Sprintf("%s: %v", e.command, e.err)
}

func (e *toolError) Unwrap() error { return e.err }

// run executes a system tool by absolute path and returns its standard output. A variable so tests
// can stand in for the account database, which a test cannot change portably.
var run = func(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.Output()
	if err != nil {
		failure := &toolError{command: path + " " + strings.Join(args, " "), err: err}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			failure.Stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		return string(out), failure
	}
	return string(out), nil
}

// lookPath finds the first of candidates that exists. A variable for tests.
var lookPath = func(candidates ...string) (string, bool) {
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, true
		}
	}
	return "", false
}

// Ensure returns the group's gid, creating it as a system group when it does not exist.
func Ensure(name string) (gid int, created bool, err error) {
	if err := validName(name); err != nil {
		return 0, false, err
	}
	gid, found, err := Lookup(name)
	if err != nil {
		return 0, false, err
	}
	if found {
		return gid, false, nil
	}
	if err := create(name); err != nil {
		return 0, false, fmt.Errorf("create group %s: %w", name, err)
	}
	gid, found, err = Lookup(name)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, fmt.Errorf("group %s was created but cannot be found", name)
	}
	return gid, true, nil
}

// Lookup returns the group's gid and whether it exists.
func Lookup(name string) (gid int, found bool, err error) {
	if err := validName(name); err != nil {
		return 0, false, err
	}
	return lookup(name)
}

// AddMember adds account to the group, which must exist. Adding an account that is already a member
// succeeds. The account may come from a directory service, so it is resolved by the system's own
// tools, never by os/user.
func AddMember(group, account string) error {
	if err := validName(group); err != nil {
		return err
	}
	if err := validAccount(account); err != nil {
		return err
	}
	return addMember(group, account)
}

// validAccount accepts the account names local and directory accounts use, so a name is never read
// as an option, a path or a second argument by the tools it is passed to. Every platform accepts
// letters, digits and . _ - $ @. Windows also accepts one DOMAIN\name separator and, because OOBE
// makes a local account's name from what the person typed (`John Smith`, `José`), letters and digits
// from any script, combining marks, interior spaces and an apostrophe. The tools get each name as one
// argv entry (exec.Command, never a shell), so a space cannot split it; a leading - or / (net.exe's
// option prefix) is refused so it is never read as an option, and a comma never, because
// --log-readers is a comma-separated list.
func validAccount(account string) error {
	if account == "" || len(account) > 256 || strings.HasPrefix(account, "-") ||
		strings.TrimSpace(account) != account {
		return fmt.Errorf("invalid account name %q", account)
	}
	backslashes := 0
	for _, r := range account {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '$' || r == '@':
		case r == '\\' && accountWindowsNames:
			backslashes++
		case accountWindowsNames && (r == ' ' || r == '\'' ||
			r > unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))):
		default:
			return fmt.Errorf("invalid account name %q", account)
		}
	}
	if backslashes > 1 || strings.HasPrefix(account, "\\") || strings.HasSuffix(account, "\\") {
		return fmt.Errorf("invalid account name %q", account)
	}
	if backslashes == 1 {
		domain, name, _ := strings.Cut(account, "\\")
		if strings.TrimSpace(domain) != domain || strings.TrimSpace(name) != name {
			return fmt.Errorf("invalid account name %q", account)
		}
	}
	return nil
}

// validName accepts only what both platforms accept as a group name, so a name is never read as an
// option or a path by the tools it is passed to.
func validName(name string) error {
	if name == "" || len(name) > 32 || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid group name %q", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("invalid group name %q", name)
		}
	}
	return nil
}

func parseGID(field string) (int, error) {
	gid, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil || gid < 0 {
		return 0, fmt.Errorf("unexpected gid %q", field)
	}
	return gid, nil
}

// errMemberInAlias is ERROR_MEMBER_IN_ALIAS: the account is already a member of the local group.
const errMemberInAlias = 1378

// netErrorNumber matches the number in net.exe's "System error N has occurred." In other display
// languages the sentence is translated but the number is the same, so only the number is read.
var netErrorNumber = regexp.MustCompile(`(?:^|[^0-9])([0-9]{1,5})(?:[^0-9]|$)`)

// netSystemError returns the system error number net.exe printed for a failed command, or 0. It is
// used on Windows and lives here so every platform runs its test. It reads only what net.exe wrote
// to standard error, never the command line (which holds the account name and the path), and
// ignores the account name wherever net.exe echoes it, so a name such as svc1378 can never pass for
// an error number.
func netSystemError(err error, account string) int {
	var failure *toolError
	if !errors.As(err, &failure) || failure.Stderr == "" {
		return 0
	}
	stderr := failure.Stderr
	if account != "" {
		stderr = strings.ReplaceAll(stderr, account, " ")
		if _, name, ok := strings.Cut(account, `\`); ok && name != "" {
			stderr = strings.ReplaceAll(stderr, name, " ")
		}
	}
	match := netErrorNumber.FindStringSubmatch(stderr)
	if match == nil {
		return 0
	}
	n, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return 0
	}
	return n
}
