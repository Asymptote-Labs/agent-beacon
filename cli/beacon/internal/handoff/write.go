package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteBrief stores a rendered brief under dir and returns its path. See WriteBriefAt.
func WriteBrief(dir string, brief Brief) (string, error) {
	path := filepath.Join(dir, BriefFileName(brief.Session, brief.GeneratedAt))
	return path, WriteBriefAt(path, brief)
}

// WriteBriefAt stores a rendered brief at path. Its directory is private to the user (0700) and the
// file is 0600, because a brief carries prompt text and command output. An existing file is never
// overwritten.
func WriteBriefAt(path string, brief Brief) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create handoff directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone; a brief directory someone widened is
	// narrowed again before anything is written into it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict handoff directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create handoff brief: %w", err)
	}
	if _, err := f.WriteString(brief.Render()); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write handoff brief: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("write handoff brief: %w", err)
	}
	return nil
}

// BriefFileName names a brief after its runtime, session and the moment it was written. Session ids
// are reduced to characters every filesystem accepts; Cline subagent ids carry colons.
func BriefFileName(session Session, at time.Time) string {
	var id strings.Builder
	for _, r := range session.ID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			id.WriteRune(r)
		default:
			id.WriteRune('_')
		}
	}
	name := strings.Trim(id.String(), ".")
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "session"
	}
	return fmt.Sprintf("%s-%s-%s.md", session.Harness, name, at.UTC().Format("20060102T150405.000Z"))
}
