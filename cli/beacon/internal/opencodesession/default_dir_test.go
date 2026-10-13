package opencodesession

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestDefaultStoreFindsProducerDataWithLegacyFallback(t *testing.T) {
	for _, mode := range []string{"home", "xdg", "legacy", "mixed", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			config := filepath.Join(home, "config")
			data := filepath.Join(home, "data")
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("APPDATA", config)
			t.Setenv("XDG_DATA_HOME", data)
			if mode == "home" {
				t.Setenv("XDG_DATA_HOME", "")
				data = filepath.Join(home, ".local", "share")
			}
			modern := filepath.Join(data, "opencode")
			legacy := filepath.Join(config, "opencode")
			makeDB := func(dir, id string) {
				path := filepath.Join(dir, "opencode.db")
				if err := os.MkdirAll(dir, 0700); err != nil {
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
			if mode != "legacy" {
				makeDB(modern, "ses_modern")
			}
			if mode == "legacy" || mode == "mixed" || mode == "explicit" {
				makeDB(legacy, "ses_legacy")
			}
			argument := ""
			if mode == "explicit" {
				argument = legacy
			}
			store, err := NewStore(argument)
			if err != nil {
				t.Fatal(err)
			}
			refs, err := store.List()
			if err != nil {
				t.Fatal(err)
			}
			want := "ses_modern"
			if mode == "legacy" || mode == "explicit" {
				want = "ses_legacy"
			}
			if len(refs) != 1 || refs[0].ID != want {
				t.Fatalf("refs=%+v, want %s (data dir %s)", refs, want, store.DataDir)
			}
		})
	}
}
