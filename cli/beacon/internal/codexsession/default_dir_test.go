package codexsession

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestDefaultCodexDirUsesLiteralProducerRoot(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Chdir(home)
	for _, tc := range []struct{ name, value string }{
		{"absolute", filepath.Join(home, "profile")},
		{"relative", "relative-profile"},
		{"literal dollar", "$PROFILE"},
		{"literal tilde", "~profile"},
		{"literal spaces", " profile "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.value, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", tc.value)
			got, err := DefaultCodexDir()
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := filepath.EvalSymlinks(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.Abs(canonical)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("DefaultCodexDir()=%q, want literal root %q", got, want)
			}
		})
	}
	t.Run("canonical directory", func(t *testing.T) {
		target := filepath.Join(home, "canonical-profile")
		if err := os.MkdirAll(target, 0700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(home, "profile-alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("owned directory symlink unavailable: %v", err)
		}
		t.Setenv("CODEX_HOME", alias)
		got, err := DefaultCodexDir()
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatal(err)
		}
		want, err = filepath.Abs(want)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("canonical root=%q, want target %q", got, want)
		}
	})
	t.Run("empty default", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		got, err := DefaultCodexDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(home, ".codex") {
			t.Fatalf("default=%q", got)
		}
	})
	t.Run("missing override", func(t *testing.T) {
		t.Setenv("CODEX_HOME", filepath.Join(home, "missing"))
		if _, err := DefaultCodexDir(); err == nil {
			t.Fatal("missing CODEX_HOME must fail rather than read another profile")
		}
	})
	t.Run("file override", func(t *testing.T) {
		path := filepath.Join(home, "file")
		if err := os.WriteFile(path, []byte("owned fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CODEX_HOME", path)
		if _, err := DefaultCodexDir(); err == nil {
			t.Fatal("file CODEX_HOME must fail rather than read another profile")
		}
	})
}
