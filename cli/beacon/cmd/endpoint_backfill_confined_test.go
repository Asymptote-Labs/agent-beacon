//go:build !windows

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// A store too large for the system backfill to copy is named with the sync command that reads it
// in place, and the stores it does copy go under the system base directory, not /tmp.
func TestSystemEndpointBackfillNamesTheSyncForAStoreTooLargeToCopy(t *testing.T) {
	home := t.TempDir()
	big := filepath.Join(home, ".cursor", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(big), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: past the cap by size, nearly nothing on disk, and refused before any byte is copied.
	if err := f.Truncate(sessionfs.MaxSnapshotBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	small := filepath.Join(home, ".hermes", "state.db")
	if err := os.MkdirAll(filepath.Dir(small), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}

	var bigErr error
	var smallSnap string
	stubBackfill(t, 1<<20, 1<<20,
		endpointBackfillRunner{Name: "Cursor", Sync: "beacon endpoint cursor sync", Run: func(endpointBackfillRun) (int, error) {
			_, bigErr = sessionfs.SQLitePath(big)
			return 0, bigErr
		}},
		endpointBackfillRunner{Name: "Hermes Agent", Sync: "beacon endpoint hermes sync", Run: func(endpointBackfillRun) (int, error) {
			var err error
			smallSnap, err = sessionfs.SQLitePath(small)
			return 0, err
		}},
	)
	scratch := endpointBackfillScratchDir()
	stubConsoleUser(t, consoleUserInfo{Username: "alice", HomeDir: home}, true, nil)

	var out strings.Builder
	reportEndpointBackfill(&out, runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), false))
	if !os.IsNotExist(bigErr) {
		t.Fatalf("an oversized store was opened: %v", bigErr)
	}
	if !strings.Contains(out.String(), big) || !strings.Contains(out.String(), "`beacon endpoint cursor sync`") {
		t.Fatalf("report does not name the oversized store and its sync command:\n%s", out.String())
	}
	if strings.Contains(out.String(), "retry with `beacon endpoint cursor sync`") {
		t.Fatalf("report also lists the oversized store as a failure:\n%s", out.String())
	}
	if !strings.HasPrefix(smallSnap, scratch+string(filepath.Separator)) {
		t.Fatalf("snapshot %s is not under the system scratch directory %s", smallSnap, scratch)
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Fatalf("snapshots survived the backfill: %v", left)
	}
}

// A system backfill reads the console user's home as root, so it confines every store read to that
// home and to files that user owns; a user backfill reads the user's own home unconfined.
func TestSystemEndpointBackfillConfinesReadsToTheConsoleUsersHome(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "mine.jsonl"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "theirs.jsonl")
	if err := os.WriteFile(secret, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(home, "planted.jsonl")
	if err := os.Symlink(secret, planted); err != nil {
		t.Fatal(err)
	}

	var mine, theirs string
	var plantedErr error
	stubBackfill(t, 1<<20, 1<<20, endpointBackfillRunner{Name: "Probe", Sync: "beacon endpoint probe sync", Run: func(r endpointBackfillRun) (int, error) {
		data, _ := sessionfs.ReadFile(filepath.Join(home, "mine.jsonl"))
		mine = string(data)
		data, plantedErr = sessionfs.ReadFile(planted)
		theirs = string(data)
		return 0, nil
	}})
	stubConsoleUser(t, consoleUserInfo{Username: "alice", HomeDir: home}, true, nil)

	var out strings.Builder
	reportEndpointBackfill(&out, runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), false))
	if mine != "mine" {
		t.Fatalf("system backfill could not read the console user's own file: %q", mine)
	}
	if theirs != "" || !os.IsNotExist(plantedErr) {
		t.Fatalf("system backfill read through a link out of the home: %q, %v", theirs, plantedErr)
	}
	if !strings.Contains(out.String(), "Skipped 1 session store path(s)") || !strings.Contains(out.String(), planted) {
		t.Fatalf("report does not name the skipped path:\n%s", out.String())
	}

	// The confinement ends with the system sweep: a user backfill follows the user's own links.
	theirs = ""
	out.Reset()
	reportEndpointBackfill(&out, runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), true))
	if theirs != "theirs" {
		t.Fatalf("user backfill was confined: %q, %v", theirs, plantedErr)
	}
	if strings.Contains(out.String(), "Skipped") {
		t.Fatalf("user backfill reported refusals:\n%s", out.String())
	}
}
