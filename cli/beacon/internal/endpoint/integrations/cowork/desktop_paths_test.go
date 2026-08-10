package cowork

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDesktopPathsForOSAnswersForEveryPlatform is why this takes goos as a parameter.
//
// Detection previously knew one path, /Applications/Claude.app, so on Windows a machine with Claude
// Desktop installed reported it as absent -- indistinguishable from one that does not have it. Taking
// goos rather than reading runtime.GOOS means every platform's answer is checkable from any machine,
// which is the convention vscodeUserDataDirsForOS already set.
func TestDesktopPathsForOSAnswersForEveryPlatform(t *testing.T) {
	t.Run("darwin looks in both Applications directories", func(t *testing.T) {
		paths := DesktopPathsForOS("darwin")
		if len(paths) != 2 {
			t.Fatalf("darwin paths = %#v, want the system and per-user Applications directories", paths)
		}
		if paths[0] != "/Applications/Claude.app" {
			t.Fatalf("darwin paths[0] = %q", paths[0])
		}
	})

	t.Run("windows reads LOCALAPPDATA", func(t *testing.T) {
		t.Setenv("LOCALAPPDATA", filepath.Join("D:", "Redirected", "Local"))
		paths := DesktopPathsForOS("windows")
		want := filepath.Join("D:", "Redirected", "Local", "AnthropicClaude")
		if len(paths) != 1 || paths[0] != want {
			t.Fatalf("windows paths = %#v, want [%q]; a redirected profile must not be missed", paths, want)
		}
	})

	t.Run("windows falls back when LOCALAPPDATA is unset", func(t *testing.T) {
		// A service or scheduled task can run with a minimal environment. Returning nothing there
		// would report Claude Desktop as absent for a reason that has nothing to do with Claude.
		t.Setenv("LOCALAPPDATA", "")
		paths := DesktopPathsForOS("windows")
		if len(paths) != 1 {
			t.Fatalf("windows paths with no LOCALAPPDATA = %#v, want one fallback path", paths)
		}
		if !strings.HasSuffix(paths[0], filepath.Join("AppData", "Local", "AnthropicClaude")) {
			t.Fatalf("fallback path = %q, want it under AppData\\Local", paths[0])
		}
	})

	t.Run("linux has nowhere to look", func(t *testing.T) {
		// Claude Desktop does not ship for Linux, and "there is nowhere to look" is a different
		// statement from "looked and did not find it".
		if paths := DesktopPathsForOS("linux"); paths != nil {
			t.Fatalf("linux paths = %#v, want none", paths)
		}
	})
}
