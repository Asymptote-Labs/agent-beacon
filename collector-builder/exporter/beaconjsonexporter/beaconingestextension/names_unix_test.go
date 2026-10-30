//go:build !windows

package beaconingestextension

import (
	"os"
	"testing"
)

func TestFirstExecutable(t *testing.T) {
	dir := t.TempDir()
	plain := dir + "/plain"
	exe := dir + "/exe"
	_ = os.WriteFile(plain, nil, 0o644)
	_ = os.WriteFile(exe, nil, 0o755)
	_ = os.Chmod(exe, 0o755)
	if got := firstExecutable([]string{dir + "/missing", plain, exe}); got != exe {
		t.Fatalf("firstExecutable = %q, want %q", got, exe)
	}
	if got := firstExecutable([]string{dir + "/missing"}); got != "" {
		t.Fatalf("firstExecutable = %q, want none", got)
	}
}
