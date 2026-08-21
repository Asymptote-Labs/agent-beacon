// Package osuser resolves local accounts in a way that survives a directory service.
//
// Beacon ships CGO_ENABLED=0, which is what makes every binary statically linked with no glibc
// version floor -- the property that lets it run on a stripped or older userland. The same flag
// selects Go's pure-Go os/user, and that implementation parses /etc/passwd directly and never
// consults NSS. On a host whose accounts come from OpenLDAP, SSSD, or AD, `user.Lookup("alice")`
// therefore fails for an account the rest of the system resolves perfectly well.
//
// That combination is not avoidable by building with cgo: glibc implements NSS by dlopen-ing
// libnss_*.so at runtime, so a statically linked glibc binary cannot consult NSS either. Static
// linking and NSS are mutually exclusive by construction. Shelling out to getent is the standard
// way out, and it keeps the static binaries intact.
//
// The distinction that matters: looking up *yourself* already works, because Go short-circuits a
// by-name lookup to Current() when the name matches, and Current() falls back to $USER/$HOME. It
// is looking up *someone else* -- which is exactly what a root-run system install must do to know
// whose agent runtime to configure -- that fails.
package osuser

import (
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"
)

// lookupTimeout bounds the NSS query.
//
// This runs inside a package postinstall, and a directory service that is slow or unreachable is
// an ordinary condition rather than an exotic one. Without a bound, `apt install` would hang on a
// network problem that has nothing to do with Beacon. Treating a timeout as "unresolved" is right:
// the caller already handles an unresolvable user, and failing the whole install over it would be
// worse than configuring nobody.
const lookupTimeout = 5 * time.Second

// Info is the subset of an account Beacon needs: who they are, and where their configuration goes.
type Info struct {
	Username string
	UID      int
	GID      int
	HomeDir  string
}

// runGetent is a var so tests can exercise the parsing and the failure modes without depending on
// the host's account database, which is the one thing a test cannot arrange portably.
var runGetent = func(ctx context.Context, name string) (string, error) {
	// Resolved explicitly so a missing getent is reported as "no NSS available" rather than as an
	// opaque exec failure, and so the absence is distinguishable from a lookup miss.
	path, err := exec.LookPath("getent")
	if err != nil {
		return "", fmt.Errorf("getent is not available: %w", err)
	}
	// argv, never a shell: the name arrives from SUDO_USER or logind, and passing it as a single
	// argument means no quoting scheme has to be correct for it to be safe.
	out, err := exec.CommandContext(ctx, path, "passwd", name).Output()
	return string(out), err
}

// Lookup resolves a username, consulting NSS when the local account database cannot answer.
//
// The ladder is ordered by cost and certainty: the local database first, since it is in-process
// and covers every ordinary host, then NSS. A caller that also has a home directory to fall back
// on should do so only after this returns an error.
func Lookup(name string) (Info, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Info{}, fmt.Errorf("no username given")
	}
	if u, err := user.Lookup(name); err == nil {
		info, convErr := fromUser(u)
		if convErr == nil {
			return info, nil
		}
		// A resolvable account with an unparseable uid is a broken database rather than a missing
		// user, but NSS may still have a usable answer, so fall through rather than fail here.
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	out, err := runGetent(ctx, name)
	if err != nil {
		return Info{}, fmt.Errorf("%q is not in the local account database and NSS could not resolve it: %w", name, err)
	}
	return parsePasswdLine(out, name)
}

func fromUser(u *user.User) (Info, error) {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return Info{}, fmt.Errorf("uid %q is not numeric: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return Info{}, fmt.Errorf("gid %q is not numeric: %w", u.Gid, err)
	}
	return Info{Username: u.Username, UID: uid, GID: gid, HomeDir: u.HomeDir}, nil
}

// parsePasswdLine reads one getent passwd record: name:passwd:uid:gid:gecos:home:shell.
//
// getent returns at most one record for a name query, but the output is scanned line by line
// anyway so a trailing newline or a stray banner cannot turn a good answer into a parse failure.
func parsePasswdLine(out, want string) (Info, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		gid, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}
		if fields[0] == "" || fields[5] == "" {
			continue
		}
		// The record's own name is preferred over the requested spelling: a directory service may
		// answer a differently-cased query with the account's canonical name, and the canonical one
		// is what ownership and re-execution should use.
		return Info{Username: fields[0], UID: uid, GID: gid, HomeDir: fields[5]}, nil
	}
	return Info{}, fmt.Errorf("no usable passwd record for %q", want)
}
