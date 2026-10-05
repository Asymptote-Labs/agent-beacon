package asymptoteobserve

import (
	"bytes"
	"encoding/json"
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
// holding a DSHSpoolGitignore that keeps it out of git.
var DSHSpoolDirRel = filepath.Join(".beacon", "dsh-spool")

// DSHSpoolGitignore is the ignore file the hook writes inside the spool directory on first
// use. It holds a single `*`, which ignores every file in the directory, itself included, so
// git never offers the spool for a commit. It lives in the spool directory rather than in the
// repository's own .gitignore or .git/info/exclude because it works the same in a plain
// checkout, a linked worktree, a submodule, and a directory that only becomes a repository
// later, and it never edits a file Beacon does not own.
const DSHSpoolGitignore = ".gitignore"

// DSHSpoolDirMode and DSHSpoolFileMode keep staged events private to the user who ran the
// session. A spool holds the same retained content as the runtime log -- prompts, command
// output, diffs -- but in a workspace, which is usually readable by more than its owner.
const (
	DSHSpoolDirMode  os.FileMode = 0o700
	DSHSpoolFileMode os.FileMode = 0o600
)

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

// CheckDSHSpoolDir reports whether a workspace's spool directory is safe to stage into or
// drain from: both <workspace>/.beacon and <workspace>/.beacon/dsh-spool must be real
// directories, not symlinks. The workspace is writable from inside the session sandbox while
// the drain runs outside it, so a symlinked spool directory would let sandboxed code point
// the drain at files the sandbox itself cannot reach. A missing directory is reported as
// os.ErrNotExist so callers can tell "nothing staged" from "refused".
func CheckDSHSpoolDir(workspace string) error {
	if workspace == "" {
		return os.ErrNotExist
	}
	dir := filepath.Join(workspace, ".beacon")
	for _, path := range []string{dir, DSHSpoolDir(workspace)} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("spool path %s is not a directory (mode %s); refusing to follow it", path, info.Mode().Type())
		}
	}
	return nil
}

// DSHSpoolFiles lists the spool data files that exist for one session, oldest first: the
// rotated archives the hook writer may have produced (.5 down to .1) before the live file.
// Lock files are not data and are never listed; they persist in the directory by design.
// Only regular files are listed: a symlink or other special file in the spool is not
// something the hook writer produces, and following it from outside the sandbox would read
// whatever it points at.
func DSHSpoolFiles(workspace, sessionID string) []string {
	base, ok := DSHSpoolPath(workspace, sessionID)
	if !ok {
		return nil
	}
	var out []string
	for i := defaultEndpointRotateArchives; i >= 1; i-- {
		candidate := base + "." + fmt.Sprintf("%d", i)
		if info, err := os.Lstat(candidate); err == nil && info.Mode().IsRegular() {
			out = append(out, candidate)
		}
	}
	if info, err := os.Lstat(base); err == nil && info.Mode().IsRegular() {
		out = append(out, base)
	}
	return out
}

// DSHSpoolPendingBytes reports how many bytes of undrained spool a workspace holds for one
// session: zero when the id is invalid or nothing was ever staged. Status and doctor use it
// as the "captured, not yet in the runtime log" signal.
func DSHSpoolPendingBytes(workspace, sessionID string) int64 {
	var total int64
	if CheckDSHSpoolDir(workspace) != nil {
		return 0
	}
	for _, path := range DSHSpoolFiles(workspace, sessionID) {
		if info, err := os.Lstat(path); err == nil {
			total += info.Size()
		}
	}
	return total
}

// VerifiedEventID returns the event.id a JSONL line claims, and whether that id is the one
// EventIDForLine derives from the same line with the id removed.
//
// Writers stamp event.id over the map-marshalled bytes they are about to write, and
// encoding/json writes map keys sorted, so re-encoding the decoded line (numbers kept
// verbatim) with event.id removed reproduces those bytes and therefore that id. A line whose
// id does not verify was not stamped by a Beacon writer from its own content -- it was
// edited, or written by something else -- and the claimed id must not be trusted, in
// particular not as proof that a later event with the same id is already recorded.
func VerifiedEventID(line []byte) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var event map[string]interface{}
	if err := decoder.Decode(&event); err != nil {
		return "", false
	}
	info, ok := event["event"].(map[string]interface{})
	if !ok {
		return "", false
	}
	claimed, _ := info["id"].(string)
	if strings.TrimSpace(claimed) == "" {
		return "", false
	}
	delete(info, "id")
	canonical, err := json.Marshal(event)
	if err != nil {
		return claimed, false
	}
	return claimed, EventIDForLine(canonical) == claimed
}

// defaultEndpointRotateArchives mirrors the hook writer's archive count (5) so the file
// enumeration here names the same set the writer rotates through. Kept local to avoid
// exporting a rotation knob from this package; the hook writer's constant is pinned by its
// own tests against this list.
const defaultEndpointRotateArchives = 5
