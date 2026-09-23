package asymptoteobserve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DSHSpoolDirRel is the workspace-relative directory where sandboxed DeepSeek Harness hook
// events are staged when the runtime log under ~/.beacon is unreachable from inside the
// session sandbox (#605). It is the only file Beacon ever writes into a DSH workspace: the
// sandbox policy confines hook writes to the session cwd, so this is the one durable
// location both the hook (inside the sandbox) and `beacon endpoint dsh sync` (outside it)
// can reach. Sync drains each file and deletes it; the directory itself is left in place,
// covered by a best-effort .git/info/exclude entry the hook writes on first use.
var DSHSpoolDirRel = filepath.Join(".beacon", "dsh-spool")

// dshSpoolMaxSessionID bounds the filename component so a hostile or malformed session id
// cannot produce an unwieldy path. Real ids are UUID-shaped; 128 is far above any of them.
const dshSpoolMaxSessionID = 128

// ValidDSHSessionIDForSpool reports whether a session id is safe to use as a spool filename
// component. The id arrives from the runtime's hook payload and from the session store, so
// both the writer and the drainer must agree on exactly which ids get a spool file: an id
// containing a path separator, a dot-only component, or anything outside the portable
// filename alphabet gets no spool at all, on both sides, rather than a sanitized name the
// other side would never look for.
func ValidDSHSessionIDForSpool(id string) bool {
	if id == "" || len(id) > dshSpoolMaxSessionID {
		return false
	}
	if id == "." || id == ".." || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// DSHSpoolDir returns the spool directory for a workspace, with no validity implied about
// any session id.
func DSHSpoolDir(workspace string) string {
	return filepath.Join(workspace, DSHSpoolDirRel)
}

// DSHSpoolPath returns the spool file for one session in one workspace. ok is false when
// the session id is not a safe filename component; the writer then records nothing to a
// spool and the drainer looks for nothing, which is the agreement ValidDSHSessionIDForSpool
// exists to keep.
func DSHSpoolPath(workspace, sessionID string) (path string, ok bool) {
	if workspace == "" || !ValidDSHSessionIDForSpool(sessionID) {
		return "", false
	}
	return filepath.Join(DSHSpoolDir(workspace), sessionID+".jsonl"), true
}

// DSHSpoolFiles lists the spool data files that exist for one session, oldest first: the
// rotated archives the hook writer may have produced (.5 down to .1) before the live file.
// Lock files are not data and are never listed; they persist in the directory by design.
func DSHSpoolFiles(workspace, sessionID string) []string {
	base, ok := DSHSpoolPath(workspace, sessionID)
	if !ok {
		return nil
	}
	var out []string
	for i := defaultEndpointRotateArchives; i >= 1; i-- {
		candidate := base + "." + fmt.Sprintf("%d", i)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			out = append(out, candidate)
		}
	}
	if info, err := os.Stat(base); err == nil && !info.IsDir() {
		out = append(out, base)
	}
	return out
}

// DSHSpoolPendingBytes reports how many bytes of undrained spool a workspace holds for one
// session: zero when the id is invalid or nothing was ever staged. Status and doctor use it
// as the "captured, not yet in the runtime log" signal.
func DSHSpoolPendingBytes(workspace, sessionID string) int64 {
	var total int64
	for _, path := range DSHSpoolFiles(workspace, sessionID) {
		if info, err := os.Stat(path); err == nil {
			total += info.Size()
		}
	}
	return total
}

// defaultEndpointRotateArchives mirrors the hook writer's archive count (5) so the file
// enumeration here names the same set the writer rotates through. Kept local to avoid
// exporting a rotation knob from this package; the hook writer's constant is pinned by its
// own tests against this list.
const defaultEndpointRotateArchives = 5
