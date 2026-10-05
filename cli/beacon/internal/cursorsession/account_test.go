package cursorsession

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func writeItemTable(t *testing.T, rows map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	for k, v := range rows {
		if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCachedAccountEmail(t *testing.T) {
	path := writeItemTable(t, map[string]string{
		"cursorAuth/cachedEmail": " dev@example.com ",
		"cursorAuth/accessToken": "secret-token",
	})
	got, err := CachedAccountEmail(path)
	if err != nil || got != "dev@example.com" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCachedAccountEmailJSONEncoded(t *testing.T) {
	path := writeItemTable(t, map[string]string{"cursorAuth/cachedEmail": `"dev@example.com"`})
	if got, _ := CachedAccountEmail(path); got != "dev@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestCachedAccountEmailAbsentOrInvalid(t *testing.T) {
	cases := map[string]string{
		"missing db":   filepath.Join(t.TempDir(), "nope.vscdb"),
		"no row":       writeItemTable(t, map[string]string{"cursorAuth/accessToken": "x"}),
		"not an email": writeItemTable(t, map[string]string{"cursorAuth/cachedEmail": "signed out"}),
		"empty":        writeItemTable(t, map[string]string{"cursorAuth/cachedEmail": ""}),
	}
	for name, path := range cases {
		got, err := CachedAccountEmail(path)
		if err != nil || got != "" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestCachedAccountEmailWithoutItemTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got, err := CachedAccountEmail(path); err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}
