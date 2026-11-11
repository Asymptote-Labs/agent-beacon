// Package sessionfs is how the session-store collectors read an agent's own session store.
//
// Unconfined, every function is the os call it names, so `beacon endpoint <runtime> sync` and a
// user-mode backfill read exactly what they always read: a person's own symlinks are their
// business.
//
// A system endpoint's install-time backfill is different. It runs as root and reads the console
// user's stores (see runEndpointBackfill in cmd), so anything that user can plant in their home is
// read with root's privileges: a symlink to another user's or root's transcripts, a FIFO that blocks
// the install forever, a device such as /dev/zero. Confine closes that for the duration of the
// sweep. While it is active, a read
//
//   - must name a path inside the console user's home, and is resolved through an os.Root opened on
//     that home, so no symlink, absolute or "..", can lead out of it at any component and there is
//     no window between checking a path and opening it;
//   - opens files non-blocking and checks the opened descriptor, so only regular files and
//     directories are read, never a FIFO, socket or device;
//   - reads only files owned by the console user, which also stops a hard link to someone else's
//     file;
//   - takes a SQLite database through a private snapshot (see SQLitePath), because the driver opens
//     paths itself.
//
// A refused path reads as absent (os.IsNotExist and errors.Is(err, fs.ErrNotExist) both hold), so
// every collector skips it the way it skips a store that is not there, and the refusal is counted so
// the backfill can say what it left out.
package sessionfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxRefusalsKept bounds the refusals a confinement remembers by path; the count is exact.
const maxRefusalsKept = 8

// Confinement is an active confinement of session-store reads to one user's home.
type Confinement struct {
	home string
	root *os.Root
	// uid is the owner every file read must have; negative skips the check (Windows).
	uid int

	mu       sync.Mutex
	refused  int
	examples []string
	scratch  string
	// snapshots maps a database path to its copy, so a store that opens one database several times
	// copies it once.
	snapshots map[string]string
	// scratchParent is where snapshot directories are made; empty means os.TempDir().
	scratchParent string
	// oversized lists databases refused as too large to snapshot, until TakeOversized.
	oversized []string
}

// Options configures a confinement.
type Options struct {
	// ScratchDir is the parent of the private directory SQLite snapshots are copied into. It
	// should be root-owned and on disk; empty means os.TempDir().
	ScratchDir string
}

var active atomic.Pointer[Confinement]

// Confine confines every read through this package to home, admitting only files owned by uid (a
// negative uid skips the ownership check), until Release. Only one confinement can be active.
func Confine(home string, uid int, opts Options) (*Confinement, error) {
	home = strings.TrimSpace(home)
	if home == "" || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("confine session reads: home %q is not an absolute path", home)
	}
	home = filepath.Clean(home)
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, fmt.Errorf("confine session reads to %s: %w", home, err)
	}
	c := &Confinement{home: home, root: root, uid: uid, scratchParent: opts.ScratchDir}
	if !active.CompareAndSwap(nil, c) {
		_ = root.Close()
		return nil, errors.New("confine session reads: a confinement is already active")
	}
	return c, nil
}

// Release ends the confinement and removes any SQLite snapshots it made.
func (c *Confinement) Release() {
	if c == nil {
		return
	}
	active.CompareAndSwap(c, nil)
	c.DropSnapshots()
	_ = c.root.Close()
}

// Refused reports how many reads the confinement refused and up to a few of their reasons.
func (c *Confinement) Refused() (int, []string) {
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused, append([]string(nil), c.examples...)
}

// DropSnapshots removes the SQLite snapshots made so far. The backfill calls it after each runtime
// so snapshots of one runtime's databases never sit on disk while the next runtime is read.
func (c *Confinement) DropSnapshots() {
	if c == nil {
		return
	}
	c.mu.Lock()
	dir := c.scratch
	c.scratch = ""
	c.snapshots = nil
	c.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// DropSnapshots drops the active confinement's SQLite snapshots; a no-op when unconfined.
func DropSnapshots() { active.Load().DropSnapshots() }

// refuse records a refused read and returns the error the caller sees: a not-exist error, so the
// path is skipped like an absent one.
func (c *Confinement) refuse(op, path, reason string) error {
	c.mu.Lock()
	c.refused++
	if len(c.examples) < maxRefusalsKept {
		c.examples = append(c.examples, path+": "+reason)
	}
	c.mu.Unlock()
	return &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
}

// rel turns an absolute path into one relative to the home, or reports that it is outside.
func (c *Confinement) rel(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	path = filepath.Clean(path)
	if path == c.home {
		return ".", true
	}
	prefix := c.home
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return strings.TrimPrefix(path, prefix), true
}

// failed turns an error from the root into what the caller sees. A path that is simply absent stays
// absent and is not a refusal; anything else (a link out of the home, a loop) is refused.
func (c *Confinement) failed(op, path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
	}
	return c.refuse(op, path, err.Error())
}

// check admits a directory, or a regular file the console user owns.
func (c *Confinement) check(op, path string, info fs.FileInfo) error {
	if info.IsDir() {
		return nil
	}
	if !info.Mode().IsRegular() {
		return c.refuse(op, path, "not a regular file ("+info.Mode().Type().String()+")")
	}
	if c.uid >= 0 {
		if owner, ok := ownerUID(info); ok && owner != c.uid {
			return c.refuse(op, path, fmt.Sprintf("owned by uid %d, not the console user (uid %d)", owner, c.uid))
		}
	}
	return nil
}

// Confined reports whether reads of path go through an active confinement: one is active and path
// lies inside its home. A caller that also reads paths outside the home (root-owned system
// locations) uses this to read only those directly.
func Confined(path string) bool {
	c := active.Load()
	if c == nil {
		return false
	}
	_, ok := c.rel(path)
	return ok
}

// Open opens a file or directory for reading.
func Open(path string) (*os.File, error) {
	c := active.Load()
	if c == nil {
		return os.Open(path)
	}
	return c.open("open", path)
}

func (c *Confinement) open(op, path string) (*os.File, error) {
	rel, ok := c.rel(path)
	if !ok {
		return nil, c.refuse(op, path, "outside the console user's home")
	}
	// Non-blocking, so a FIFO is opened and refused rather than waited on for a writer.
	f, err := c.root.OpenFile(rel, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, c.failed(op, path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, c.failed(op, path, err)
	}
	if err := c.check(op, path, info); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ReadFile reads a whole file.
func ReadFile(path string) ([]byte, error) {
	c := active.Load()
	if c == nil {
		return os.ReadFile(path)
	}
	f, err := c.open("open", path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: errors.New("is a directory")}
	}
	return io.ReadAll(f)
}

// ReadDir lists a directory sorted by name, as os.ReadDir does.
func ReadDir(path string) ([]fs.DirEntry, error) {
	c := active.Load()
	if c == nil {
		return os.ReadDir(path)
	}
	f, err := c.open("open", path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	kept := entries[:0]
	for _, entry := range entries {
		if c.admitEntry(filepath.Join(path, entry.Name()), entry) {
			kept = append(kept, entry)
		}
	}
	return kept, err
}

// admitEntry decides whether a listed entry is shown to the collector at all, so a planted entry is
// skipped while listing rather than listed and then failing as a session that cannot be read. It
// admits directories, regular files the console user owns, and symlinks that resolve inside the
// home to one of those; everything else is refused here.
func (c *Confinement) admitEntry(path string, entry fs.DirEntry) bool {
	switch {
	case entry.IsDir():
		return true
	case entry.Type()&fs.ModeSymlink != 0:
		rel, ok := c.rel(path)
		if !ok {
			_ = c.refuse("stat", path, "outside the console user's home")
			return false
		}
		info, err := c.root.Stat(rel)
		if err != nil {
			_ = c.failed("stat", path, err)
			return false
		}
		return c.check("stat", path, info) == nil
	default:
		info, err := entry.Info()
		if err != nil {
			return false
		}
		return c.check("lstat", path, info) == nil
	}
}

// Stat describes a path, following symlinks only while they stay inside the home. Confined, a
// path that is neither a directory nor a regular file the console user owns reads as absent.
func Stat(path string) (fs.FileInfo, error) {
	c := active.Load()
	if c == nil {
		return os.Stat(path)
	}
	rel, ok := c.rel(path)
	if !ok {
		return nil, c.refuse("stat", path, "outside the console user's home")
	}
	info, err := c.root.Stat(rel)
	if err != nil {
		return nil, c.failed("stat", path, err)
	}
	if err := c.check("stat", path, info); err != nil {
		return nil, err
	}
	return info, nil
}

// WalkDir walks a tree as filepath.WalkDir does: it does not descend into symlinked directories,
// and the paths it reports are absolute. Confined, the walk runs through the home's os.Root.
func WalkDir(root string, fn fs.WalkDirFunc) error {
	c := active.Load()
	if c == nil {
		return filepath.WalkDir(root, fn)
	}
	rel, ok := c.rel(root)
	if !ok {
		return fn(root, nil, c.refuse("lstat", root, "outside the console user's home"))
	}
	return fs.WalkDir(c.root.FS(), filepath.ToSlash(rel), func(p string, d fs.DirEntry, err error) error {
		abs := filepath.Join(c.home, filepath.FromSlash(p))
		if err != nil {
			err = c.failed("walk", abs, err)
		} else if d != nil && !c.admitEntry(abs, d) {
			return nil
		}
		return fn(abs, d, err)
	})
}

// SQLitePath returns the path to hand the SQLite driver for the database at path.
//
// Unconfined it is path itself. Confined, the driver cannot be trusted with the user's path: it
// opens it, and its -wal and -shm files, by name, following any symlink, and in read-only mode it
// still creates a -shm file beside the database. So the database and its -wal file are read through
// the confinement into the confinement's private scratch directory, and the driver opens that copy;
// DropSnapshots removes it. A WAL file that is refused is left out, which loses at most the writes
// not yet checkpointed.
//
// The copy is checked for consistency (see snapshotFingerprint) and retried while the runtime keeps
// changing the store underneath it. A database that does not settle, or that with its WAL is larger
// than MaxSnapshotBytes, is refused like an unreadable store; an oversized one is also reported by
// TakeOversized so the backfill can name the sync command that reads it in place.
func SQLitePath(path string) (string, error) {
	c := active.Load()
	if c == nil {
		return path, nil
	}
	return c.snapshotSQLite(path)
}

// MaxSnapshotBytes caps a database plus its WAL that a confined read copies. The whole backfill
// writes at most 24 MiB of events and a runtime at most 12 MiB, so a store past this size is mostly
// data the window does not need (Cursor's global state.vscdb carries editor state for every
// workspace and can reach gigabytes). Copying it would cost that much disk under the system base
// directory for a few megabytes of events; the user's own sync reads it in place instead.
const MaxSnapshotBytes = 512 << 20

// Snapshot tuning, variables so tests can make them small or observe the copy.
var (
	maxSnapshotBytes int64 = MaxSnapshotBytes
	// snapshotAttempts is how many times a copy is retried while the store keeps changing.
	snapshotAttempts = 4
	// betweenSnapshotCopies runs after the database is copied and before its WAL is; a test seam
	// for a checkpoint landing between the two copies.
	betweenSnapshotCopies = func(string) {}
)

// errSnapshotUnstable reports a store that changed during every copy attempt.
var errSnapshotUnstable = errors.New("changed during every copy attempt")

// snapshotFingerprint is what a copy of a database and its WAL must not see change.
//
// A WAL database copied as two files is consistent as long as the WAL was not restarted between
// the copies: a checkpoint only writes into the database pages the WAL still holds and overrides,
// and frames appended after the database was copied are either complete (a newer, consistent
// state) or fail their checksum and are ignored. A restart rewrites the WAL header with new salts
// and a new checkpoint sequence, so the 32-byte header catches it. A rollback-journal database is
// rewritten in place on every commit, which bumps the file change counter at offset 24 of its
// header; the size catches truncation by a checkpoint or a VACUUM.
type snapshotFingerprint struct {
	dbSize    int64
	dbCounter [4]byte
	walExists bool
	walHeader [32]byte
}

func (c *Confinement) fingerprint(db *os.File, path string) (snapshotFingerprint, error) {
	var fp snapshotFingerprint
	info, err := db.Stat()
	if err != nil {
		return fp, err
	}
	fp.dbSize = info.Size()
	if _, err := db.ReadAt(fp.dbCounter[:], 24); err != nil && !errors.Is(err, io.EOF) {
		return fp, err
	}
	wal, err := c.open("open", path+"-wal")
	if err != nil {
		return fp, nil
	}
	defer wal.Close()
	fp.walExists = true
	if _, err := wal.ReadAt(fp.walHeader[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return fp, err
	}
	return fp, nil
}

func (c *Confinement) snapshotSQLite(path string) (string, error) {
	c.mu.Lock()
	cached, ok := c.snapshots[path]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}
	src, err := c.open("open", path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if info, err := src.Stat(); err != nil {
		return "", err
	} else if info.IsDir() {
		return "", c.refuse("open", path, "a directory, not a database")
	}
	dir, err := c.snapshotDir()
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < snapshotAttempts; attempt++ {
		before, err := c.fingerprint(src, path)
		if err != nil {
			return "", c.failed("open", path, err)
		}
		size := before.dbSize
		if before.walExists {
			if wal, err := c.root.Stat(mustRel(c, path+"-wal")); err == nil {
				size += wal.Size()
			}
		}
		if size > maxSnapshotBytes {
			return "", c.oversize(path, size)
		}
		target, err := os.MkdirTemp(dir, "db-")
		if err != nil {
			return "", err
		}
		copyPath := filepath.Join(target, filepath.Base(path))
		copied, err := c.copySnapshot(src, path, copyPath)
		if errors.Is(err, errSnapshotTooLarge) {
			_ = os.RemoveAll(target)
			return "", c.oversize(path, copied)
		}
		if err != nil {
			_ = os.RemoveAll(target)
			return "", c.failed("open", path, err)
		}
		after, err := c.fingerprint(src, path)
		if err == nil && after == before {
			c.mu.Lock()
			if c.snapshots == nil {
				c.snapshots = map[string]string{}
			}
			c.snapshots[path] = copyPath
			c.mu.Unlock()
			return copyPath, nil
		}
		_ = os.RemoveAll(target)
	}
	return "", c.refuse("open", path, errSnapshotUnstable.Error())
}

// mustRel is rel for a path already known to be inside the home.
func mustRel(c *Confinement, path string) string {
	rel, _ := c.rel(path)
	return rel
}

var errSnapshotTooLarge = errors.New("snapshot over the size limit")

// copySnapshot copies the database and then its WAL, if there is one the confinement admits,
// stopping once the two together pass maxSnapshotBytes. It reports how many bytes it copied.
func (c *Confinement) copySnapshot(src *os.File, path, copyPath string) (int64, error) {
	remaining := maxSnapshotBytes
	n, err := copyInto(copyPath, io.NewSectionReader(src, 0, remaining+1))
	if err != nil {
		return n, err
	}
	if n > remaining {
		return n, errSnapshotTooLarge
	}
	remaining -= n
	betweenSnapshotCopies(path)
	wal, err := c.open("open", path+"-wal")
	if err != nil {
		return n, nil
	}
	defer wal.Close()
	w, err := copyInto(copyPath+"-wal", io.NewSectionReader(wal, 0, remaining+1))
	n += w
	if err != nil {
		return n, err
	}
	if w > remaining {
		return n, errSnapshotTooLarge
	}
	return n, nil
}

// oversize refuses a store too large to snapshot and remembers it for TakeOversized.
func (c *Confinement) oversize(path string, size int64) error {
	c.mu.Lock()
	c.oversized = append(c.oversized, path)
	c.mu.Unlock()
	return c.refuse("open", path, fmt.Sprintf("%.1f MiB with its WAL, over the %.1f MiB a system backfill copies", float64(size)/(1<<20), float64(maxSnapshotBytes)/(1<<20)))
}

// TakeOversized returns, and forgets, the databases refused since the last call because they were
// too large to snapshot. The backfill calls it after each runtime to name that runtime's sync
// command, which reads the store in place as the user. Nil when unconfined.
func TakeOversized() []string {
	c := active.Load()
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.oversized
	c.oversized = nil
	return out
}

// snapshotDir creates the confinement's scratch directory: a fresh 0700 directory under the
// configured parent (Options.ScratchDir, which the system backfill puts under the root-owned system
// base directory rather than a possibly memory-backed /tmp), or under os.TempDir() when none is set.
func (c *Confinement) snapshotDir() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scratch != "" {
		return c.scratch, nil
	}
	parent := c.scratchParent
	if parent == "" {
		parent = os.TempDir()
	} else {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", fmt.Errorf("create SQLite snapshot directory: %w", err)
		}
		removeStaleSnapshotDirs(parent, time.Now())
	}
	dir, err := os.MkdirTemp(parent, snapshotDirPrefix)
	if err != nil {
		return "", fmt.Errorf("create SQLite snapshot directory: %w", err)
	}
	c.scratch = dir
	return dir, nil
}

const snapshotDirPrefix = "beacon-backfill-sqlite-"

// staleSnapshotAge is how old a snapshot directory left in the scratch parent must be before a
// later sweep removes it. A sweep cleans up its own copies when it ends normally, but a run killed
// mid-copy (Ctrl+C, an installer timeout) cannot, and its copies are private user data. The age
// keeps a concurrent sweep in another process from losing the directory it is reading.
const staleSnapshotAge = time.Hour

// removeStaleSnapshotDirs deletes snapshot directories older than staleSnapshotAge under parent.
// Only the configured scratch parent is swept, never os.TempDir().
func removeStaleSnapshotDirs(parent string, now time.Time) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), snapshotDirPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleSnapshotAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
	}
}

func copyInto(path string, src io.Reader) (int64, error) {
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(dst, src)
	if err != nil {
		_ = dst.Close()
		return n, err
	}
	return n, dst.Close()
}
