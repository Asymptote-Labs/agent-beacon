package opencodesession

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestDefaultStoreFindsOpenCodeData(t *testing.T) {
	for _, tc := range []struct{ name, xdgData, opencodeDB, explicit, dbRel string }{
		{name: "home", dbRel: ".local/share/opencode/opencode.db"},
		{name: "xdg", xdgData: "xdg", dbRel: "xdg/opencode/opencode.db"},
		{name: "relative OPENCODE_DB", opencodeDB: "work.db", dbRel: ".local/share/opencode/work.db"},
		{name: "absolute OPENCODE_DB", opencodeDB: "elsewhere/custom.db", dbRel: "elsewhere/custom.db"},
		{name: "explicit data dir", explicit: "config/opencode", dbRel: "config/opencode/opencode.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			t.Setenv("XDG_DATA_HOME", "")
			if tc.xdgData != "" {
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, tc.xdgData))
			}
			t.Setenv("OPENCODE_DB", tc.opencodeDB)
			if tc.name == "absolute OPENCODE_DB" {
				t.Setenv("OPENCODE_DB", filepath.Join(home, tc.opencodeDB))
			}
			// A config-directory store is never the default: OpenCode does not write sessions there.
			writeSessionDB(t, filepath.Join(home, ".config", "opencode", "opencode.db"), "ses_config")
			writeSessionDB(t, filepath.Join(home, filepath.FromSlash(tc.dbRel)), "ses_want")

			argument := ""
			if tc.explicit != "" {
				argument = filepath.Join(home, filepath.FromSlash(tc.explicit))
			}
			store, err := NewStore(argument)
			if err != nil {
				t.Fatal(err)
			}
			refs, err := store.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != 1 || refs[0].ID != "ses_want" {
				t.Fatalf("refs=%+v, want ses_want (sqlite %s)", refs, store.SQLitePath)
			}
		})
	}
}

func writeSessionDB(t *testing.T, path, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE session(id TEXT PRIMARY KEY,title TEXT,directory TEXT,time_created INTEGER,time_updated INTEGER)`)
	mustExec(t, db, `INSERT INTO session VALUES(?, 'Owned session', '/work/project', 1791882000000, 1791882000200)`, id)
}
