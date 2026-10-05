package cursorsession

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// cachedEmailKey is the ItemTable row where Cursor keeps the email of the account signed in to
// this install. The same table holds Cursor's access and refresh tokens under neighbouring
// cursorAuth/ keys; this reads exactly one key by equality and nothing else, so no credential is
// ever loaded.
const cachedEmailKey = "cursorAuth/cachedEmail"

// CachedAccountEmail returns the email of the Cursor account signed in on this machine, read from
// Cursor's global state.vscdb. It returns "" with no error when the database or the row does not
// exist, which is what a machine without Cursor or with Cursor signed out looks like.
func CachedAccountEmail(globalDBPath string) (string, error) {
	if strings.TrimSpace(globalDBPath) == "" {
		globalDBPath = DefaultGlobalDBPath()
	}
	if !fileExists(globalDBPath) {
		return "", nil
	}
	db, err := openSQLiteReadOnly(globalDBPath)
	if err != nil {
		return "", fmt.Errorf("open Cursor global storage: %w", err)
	}
	defer db.Close()

	var value []byte
	err = db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, cachedEmailKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return "", nil
		}
		return "", fmt.Errorf("read Cursor account email: %w", err)
	}
	email := strings.TrimSpace(string(value))
	// Some VS Code storage writers JSON-encode string values.
	if strings.HasPrefix(email, `"`) {
		var decoded string
		if json.Unmarshal([]byte(email), &decoded) == nil {
			email = strings.TrimSpace(decoded)
		}
	}
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n") {
		return "", nil
	}
	return email, nil
}
