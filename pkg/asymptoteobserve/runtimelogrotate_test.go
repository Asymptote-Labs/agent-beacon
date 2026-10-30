package asymptoteobserve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// rotationFixture writes a live log holding "live" and archives 1..n holding their own number, so
// every file's content says where it started.
func rotationFixture(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(path, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.%d", path, i), []byte(fmt.Sprint(i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// rotationState maps "live", "1" .. "archives" (and any later number present) to file contents.
func rotationState(t *testing.T, path string, archives int) map[string]string {
	t.Helper()
	state := map[string]string{}
	read := func(name, file string) {
		data, err := os.ReadFile(file)
		if err == nil {
			state[name] = string(data)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	read("live", path)
	for i := 1; i <= archives+2; i++ {
		read(fmt.Sprint(i), fmt.Sprintf("%s.%d", path, i))
	}
	return state
}

func assertRotationState(t *testing.T, got, want map[string]string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("files after rotation = %v, want %v", got, want)
	}
}

func withRotationSeams(t *testing.T, probe func(string) error, rename func(string, string) error) {
	t.Helper()
	oldProbe, oldRename := runtimeLogRotationProbe, runtimeLogRotationRename
	if probe != nil {
		runtimeLogRotationProbe = probe
	}
	if rename != nil {
		runtimeLogRotationRename = rename
	}
	t.Cleanup(func() { runtimeLogRotationProbe, runtimeLogRotationRename = oldProbe, oldRename })
}

func TestRotateRuntimeLogMovesEveryArchiveUpAndDropsTheOldest(t *testing.T) {
	path := rotationFixture(t, 5)
	if err := os.WriteFile(fmt.Sprintf("%s.7", path), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RotateRuntimeLog(path, 4, 5, 1); err != nil {
		t.Fatal(err)
	}
	assertRotationState(t, rotationState(t, path, 5), map[string]string{
		"1": "live", "2": "1", "3": "2", "4": "3", "5": "4",
	})
}

func TestRotateRuntimeLogLeavesASmallOrMissingLogAlone(t *testing.T) {
	path := rotationFixture(t, 2)
	if err := RotateRuntimeLog(path, 100, 5, 1); err != nil {
		t.Fatal(err)
	}
	assertRotationState(t, rotationState(t, path, 5), map[string]string{"live": "live", "1": "1", "2": "2"})
	if err := RotateRuntimeLog(path+".missing", 1, 5, 1); err != nil {
		t.Fatalf("a missing log is not an error: %v", err)
	}
}

// A file the rotation would move but cannot (on Windows, one held open without delete sharing)
// defers the whole rotation. Before the check, the archives had already moved up when the live
// file's rename was refused, so each further attempt deleted one more archive and the event was
// dropped.
func TestRotateRuntimeLogMovesNothingWhenAFileIsHeld(t *testing.T) {
	for _, held := range []string{"", ".3", ".5"} {
		t.Run("held"+held, func(t *testing.T) {
			path := rotationFixture(t, 5)
			before := rotationState(t, path, 5)
			withRotationSeams(t, func(p string) error {
				if p == path+held {
					return errors.New("held open")
				}
				return nil
			}, nil)
			err := RotateRuntimeLog(path, 4, 5, 1)
			if !errors.Is(err, ErrRuntimeLogRotationDeferred) {
				t.Fatalf("RotateRuntimeLog = %v, want ErrRuntimeLogRotationDeferred", err)
			}
			assertRotationState(t, rotationState(t, path, 5), before)
		})
	}
}

// A rename that fails after the check passed (a reader opened the file in between) leaves a gap.
// The next rotation fills it rather than removing the oldest archive again, so the two together end
// exactly where one clean rotation would.
func TestRotateRuntimeLogFillsTheGapAFailedRotationLeft(t *testing.T) {
	path := rotationFixture(t, 5)
	withRotationSeams(t, nil, func(from, to string) error {
		if from == path+".2" {
			return errors.New("opened by a reader")
		}
		return os.Rename(from, to)
	})
	if err := RotateRuntimeLog(path, 4, 5, 1); err == nil {
		t.Fatal("a refused rename must be reported")
	}
	assertRotationState(t, rotationState(t, path, 5), map[string]string{
		"live": "live", "1": "1", "2": "2", "4": "3", "5": "4",
	})

	runtimeLogRotationRename = os.Rename
	if err := RotateRuntimeLog(path, 4, 5, 1); err != nil {
		t.Fatal(err)
	}
	assertRotationState(t, rotationState(t, path, 5), map[string]string{
		"1": "live", "2": "1", "3": "2", "4": "3", "5": "4",
	})
}

// os.Rename replaces its destination on every platform, so a rotation renames only into a slot that
// is free when it gets there. A name that appears in between is kept and the rotation waits.
func TestRotateRuntimeLogNeverRenamesOverAnArchive(t *testing.T) {
	path := rotationFixture(t, 0)
	withRotationSeams(t, func(p string) error {
		if p == path {
			if err := os.WriteFile(path+".1", []byte("appeared"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}, nil)
	err := RotateRuntimeLog(path, 2, 5, 1)
	if !errors.Is(err, ErrRuntimeLogRotationDeferred) {
		t.Fatalf("RotateRuntimeLog = %v, want ErrRuntimeLogRotationDeferred", err)
	}
	assertRotationState(t, rotationState(t, path, 5), map[string]string{"live": "live", "1": "appeared"})
}
