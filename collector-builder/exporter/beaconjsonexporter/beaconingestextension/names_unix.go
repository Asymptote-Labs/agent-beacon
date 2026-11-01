//go:build !windows

package beaconingestextension

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// resolveName names a uid. The collector is built with CGO_ENABLED=0, whose os/user reads /etc/passwd and never asks NSS or
// Directory Services, so accounts from LDAP, SSSD, AD or macOS's own directory would come back
// unnamed. `id -nu <uid>` asks the system's own resolver on both Linux and macOS and is the
// fallback.

// idCommands are the absolute paths tried for id(1). The collector runs as root, so PATH is not
// consulted.
var idCommands = []string{"/usr/bin/id", "/bin/id"}

// firstExecutable is the first of paths that exists as an executable file.
func firstExecutable(paths []string) string {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

func resolveName(id string) string {
	if u, err := user.LookupId(id); err == nil && validName(u.Username) {
		return u.Username
	}
	// id(1) is tried once: on merged-/usr systems both paths are the same binary, and running it
	// again behind a directory service that did not answer would only double the wait.
	path := firstExecutable(idCommands)
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), nameLookupTimeout)
	out, err := exec.CommandContext(ctx, path, "-nu", id).Output()
	cancel()
	if err != nil {
		return ""
	}
	if name := strings.TrimSpace(string(out)); validName(name) {
		return name
	}
	return ""
}
