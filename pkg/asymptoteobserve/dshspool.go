package asymptoteobserve

import (
	"path/filepath"
	"strings"
)

// DSHSpoolRootRel is the workspace-relative directory where sandboxed DeepSeek Harness hooks stage
// events when the runtime log under ~/.beacon is unreachable from inside the session sandbox
// (#605). The sandbox confines hook writes to the session's working directory, so this is the one
// durable location both the hook (inside the sandbox) and `beacon endpoint dsh sync` (outside it)
// can reach. Each session gets its own directory below it, holding a localingest.Spool.
var DSHSpoolRootRel = filepath.Join(".beacon", "dsh-spool")

// dshSpoolMaxSessionID bounds the directory name so a hostile or malformed session id cannot
// produce an unwieldy path. Real ids are UUID-shaped; 128 is far above any of them.
const dshSpoolMaxSessionID = 128

// ValidDSHSessionIDForSpool reports whether a session id is safe to use as a spool directory name.
// The id arrives from the runtime's hook payload and from the session store, so the writer and the
// drainer must agree on exactly which ids get a spool: an id containing a path separator, a leading
// dot, or anything outside the portable filename alphabet gets no spool at all, on both sides,
// rather than a sanitized name the other side would never look for.
func ValidDSHSessionIDForSpool(id string) bool {
	if id == "" || len(id) > dshSpoolMaxSessionID || strings.HasPrefix(id, ".") {
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

// DSHSpoolDir is the spool directory for one session in one workspace. ok is false when the
// workspace is empty or the session id is not a safe directory name; the writer then stages
// nothing and the drainer looks for nothing, which is the agreement ValidDSHSessionIDForSpool
// exists to keep.
func DSHSpoolDir(workspace, sessionID string) (dir string, ok bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || !ValidDSHSessionIDForSpool(sessionID) {
		return "", false
	}
	return filepath.Join(workspace, DSHSpoolRootRel, sessionID), true
}
