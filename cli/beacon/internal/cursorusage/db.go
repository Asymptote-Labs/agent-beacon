// Package cursorusage extracts runtime-recorded token usage from Cursor's
// local state store (state.vscdb) and converts it into canonical Beacon
// token.usage events. Cursor's hook payloads carry no token counts today, and
// the store is the only local, offline source of real per-generation usage.
//
// The store is undocumented and its layout has shifted across Cursor
// versions, so extraction is strictly best-effort: unparseable rows are
// skipped and counted, generations without recorded token counts are skipped
// (never estimated), and the whole package is read-only against a private
// snapshot copy of the database.
package cursorusage

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite"
)

// DefaultDBPath returns the platform-specific location of Cursor's global
// state database.
func DefaultDBPath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb"), nil
	default:
		if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
			return filepath.Join(configDir, "Cursor", "User", "globalStorage", "state.vscdb"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb"), nil
	}
}

// OpenSnapshot copies the database (and its WAL sidecar when present) to a
// private temp directory and opens the copy. Reading a snapshot avoids lock
// contention with a running Cursor and never touches the live files; WAL
// recovery happens against the copy. cleanup closes the handle and removes
// the snapshot; it is safe to call even when an error is returned.
func OpenSnapshot(path string) (db *sql.DB, cleanup func(), err error) {
	tmpDir, err := os.MkdirTemp("", "beacon-cursor-usage-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup = func() {
		if db != nil {
			_ = db.Close()
		}
		_ = os.RemoveAll(tmpDir)
	}
	snapshot := filepath.Join(tmpDir, "state.vscdb")
	if err := copyFile(path, snapshot); err != nil {
		return nil, cleanup, fmt.Errorf("snapshot state.vscdb: %w", err)
	}
	// The WAL may hold committed rows not yet checkpointed into the main file.
	// Its absence is normal (rollback-journal mode or a fresh checkpoint).
	if err := copyFile(path+"-wal", snapshot+"-wal"); err != nil && !os.IsNotExist(err) {
		return nil, cleanup, fmt.Errorf("snapshot state.vscdb-wal: %w", err)
	}
	db, err = sql.Open("sqlite", snapshot)
	if err != nil {
		return nil, cleanup, err
	}
	if err := db.Ping(); err != nil {
		return nil, cleanup, fmt.Errorf("open snapshot: %w", err)
	}
	return db, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
