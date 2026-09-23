package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
)

// ErrRetentionWindowFull is returned by a guarded append whose rotation would discard the file
// holding the guard's first event. Nothing is written or rotated when it is returned.
var ErrRetentionWindowFull = errors.New("the runtime log's rotation window is full of this run's own output")

// RetentionGuard keeps one run of appends -- a session backfill sweep, say -- from rotating its
// own output out of the runtime log, and reports how much of that output the log still holds.
//
// The log keeps the live file plus a fixed number of archives, and every rotation deletes the
// oldest archive. A run that writes more than that window used to rotate its own first events away
// before it finished, so a backfill of 595,640 events kept 27,550 and still reported success
// (#619). With a guard, the append whose rotation would delete the file holding the run's first
// event fails with ErrRetentionWindowFull instead, and the caller can stop where it is and leave
// the rest for the next run.
//
// The guard counts the distinct files its appends landed in. That sees rotations by other writers
// too (the hooks and the collector share the log), but only after the fact: another process can
// still rotate the window over during the run, which is what Retained reports.
//
// The zero value is ready to use. A guard is not safe for concurrent use; give each run its own.
type RetentionGuard struct {
	path     string
	archives int
	segments []retentionSegment
}

// retentionSegment is a run of consecutive guarded appends that landed in one file, identified by
// the file and by the first of those lines: where it starts and a digest of its bytes. The file
// identity alone is not enough, because filesystems recycle the inode of a deleted archive for the
// next file created, and a recycled inode would make a rotated-out segment look retained.
//
// file is nil when the file could not be identified; its events are then assumed retained rather
// than reported lost on no evidence.
type retentionSegment struct {
	file   os.FileInfo
	offset int64
	length int
	digest [sha256.Size]byte
	end    int64
	events int
}

// Written is the number of events the guard's appends wrote. Appends the writer suppressed as
// duplicates wrote nothing and are not counted.
func (g *RetentionGuard) Written() int {
	if g == nil {
		return 0
	}
	n := 0
	for _, s := range g.segments {
		n += s.events
	}
	return n
}

// Retained is the number of the guard's written events still in a file the rotation contract
// keeps: the live log or one of its archives. Written minus Retained is what was rotated out.
func (g *RetentionGuard) Retained() int {
	if g == nil || len(g.segments) == 0 {
		return 0
	}
	var kept []os.FileInfo
	var keptPaths []string
	for _, p := range retainedPaths(g.path, g.archives) {
		if info, err := os.Stat(p); err == nil {
			kept = append(kept, info)
			keptPaths = append(keptPaths, p)
		}
	}
	n := 0
	for _, s := range g.segments {
		if s.file == nil {
			n += s.events
			continue
		}
		for i, info := range kept {
			if os.SameFile(s.file, info) && s.firstLineAt(keptPaths[i]) {
				n += s.events
				break
			}
		}
	}
	return n
}

// firstLineAt reports whether path still holds the segment's first line where it was written.
func (s retentionSegment) firstLineAt(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, s.length)
	if _, err := f.ReadAt(buf, s.offset); err != nil {
		return false
	}
	digest := sha256.Sum256(buf)
	return bytes.Equal(digest[:], s.digest[:])
}

// rotationWouldDiscardOwnOutput reports whether a rotation now would delete the file holding the
// guard's first event. The guard's files are, oldest first, at .N ... .1 and the live path, and a
// rotation deletes .archives, so once the guard has written into archives+1 files the oldest of
// them is at .archives. Before the guard's first write there is nothing of its own to lose.
func (g *RetentionGuard) rotationWouldDiscardOwnOutput(archives int) bool {
	if archives < 1 {
		archives = DefaultRotateArchives
	}
	return len(g.segments) >= archives+1
}

// record notes one written line. info is the file it was appended to, stat'd after the write under
// the log's lock, so its size is where the line ends.
func (g *RetentionGuard) record(path string, archives int, line []byte, info os.FileInfo, statErr error) {
	if archives < 1 {
		archives = DefaultRotateArchives
	}
	g.path = path
	g.archives = archives
	last := len(g.segments) - 1
	if statErr != nil {
		// The write happened, so it counts; attribute it to the file the previous append used,
		// which is where it almost certainly went.
		if last >= 0 {
			g.segments[last].events++
			return
		}
		g.segments = append(g.segments, retentionSegment{events: 1})
		return
	}
	end := info.Size()
	// Same file as the previous append, and grown past where that append ended: a file that was
	// replaced by one with a recycled inode starts over from empty.
	if last >= 0 && g.segments[last].file != nil && os.SameFile(g.segments[last].file, info) && end > g.segments[last].end {
		g.segments[last].events++
		g.segments[last].end = end
		return
	}
	g.segments = append(g.segments, retentionSegment{
		file:   info,
		offset: end - int64(len(line)),
		length: len(line),
		digest: sha256.Sum256(line),
		end:    end,
		events: 1,
	})
}

// retainedPaths is RetainedLogPaths for an explicit archive count.
func retainedPaths(path string, archives int) []string {
	if path == "" {
		return nil
	}
	paths := make([]string, 0, archives+1)
	paths = append(paths, path)
	for i := 1; i <= archives; i++ {
		paths = append(paths, path+fmt.Sprintf(".%d", i))
	}
	return paths
}
