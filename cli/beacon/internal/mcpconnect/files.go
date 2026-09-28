package mcpconnect

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// configFile is a harness config as read from disk.
type configFile struct {
	path   string // the path Beacon reports and records
	real   string // the file actually written, after following symlinks
	text   string
	exists bool
	mode   os.FileMode
}

// readConfig reads a harness config. A symlinked config (a dotfiles manager's, say) is read and
// later written through the link, so the link survives and the file it points at is the one that
// changes.
func readConfig(path string) (configFile, error) {
	cf := configFile{path: path, real: path, mode: 0o600}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		cf.real = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return cf, err
	}
	info, err := os.Stat(cf.real)
	if errors.Is(err, os.ErrNotExist) {
		return cf, nil
	}
	if err != nil {
		return cf, err
	}
	if info.IsDir() {
		return cf, fmt.Errorf("%s is a directory", path)
	}
	data, err := os.ReadFile(cf.real)
	if err != nil {
		return cf, err
	}
	cf.text, cf.exists, cf.mode = string(data), true, info.Mode().Perm()
	return cf, nil
}

// write replaces the config atomically: a temporary file in the same directory, given the
// existing file's mode, renamed over it. A reader never sees half a config. A file Beacon creates
// is 0600, because several of these configs are where users keep API keys.
func (cf configFile) write(text string) error {
	dir := filepath.Dir(cf.real)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(cf.real)+".beacon-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, cf.mode); err != nil {
		return err
	}
	return os.Rename(tmpName, cf.real)
}

// remove deletes a config file Beacon created.
func (cf configFile) remove() error {
	err := os.Remove(cf.real)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// backup copies the current contents next to the file, following the naming the endpoint
// installers already use (<file>.beacon.<UTC timestamp>.bak, 0600). An empty or missing file has
// nothing to keep and gets no backup.
func (cf configFile) backup(now time.Time) (string, error) {
	if !cf.exists || cf.text == "" {
		return "", nil
	}
	base := fmt.Sprintf("%s.beacon.%s", cf.real, now.UTC().Format("20060102T150405Z"))
	path := base + ".bak"
	for i := 1; ; i++ {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		path = fmt.Sprintf("%s.%d.bak", base, i)
	}
	if err := os.WriteFile(path, []byte(cf.text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
