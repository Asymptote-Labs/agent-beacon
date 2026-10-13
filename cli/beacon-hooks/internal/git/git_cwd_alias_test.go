package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentBranchFollowsThePhysicalWorkingDirectory(t *testing.T) {
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("native git unavailable")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err = os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "none"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	initRepo := func(name, branch string) string {
		path := filepath.Join(base, name)
		cmd := exec.Command(binary, "init", "-b", branch, path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("init real repository: %v %s", err, out)
		}
		remote := exec.Command(binary, "-C", path, "remote", "add", "origin", "https://github.com/acme/"+name+".git")
		if out, err := remote.CombinedOutput(); err != nil {
			t.Fatalf("add origin: %v %s", err, out)
		}
		return path
	}
	target := initRepo("target", "feature/target")
	outer := initRepo("outer", "feature/outer")
	nested := filepath.Join(target, "pkg", "deep")
	if err = os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, target string }{
		{"physical", nested, ""},
		{"external alias", filepath.Join(base, "workspace"), nested},
		{"alias inside another repo", filepath.Join(outer, "workspace"), nested},
		{"repository root alias", filepath.Join(base, "root-alias"), target},
		{"relative alias inside another repo", filepath.Join(outer, "relative-workspace"), filepath.Join("..", "target", "pkg", "deep")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.target != "" {
				if err := os.Symlink(tc.target, tc.path); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			}
			out, err := exec.Command(binary, "-C", tc.path, "symbolic-ref", "--short", "HEAD").CombinedOutput()
			if err != nil {
				t.Fatalf("native git oracle: %v %s", err, out)
			}
			want := strings.TrimSpace(string(out))
			if got := CurrentBranch(tc.path); got != want {
				t.Fatalf("CurrentBranch=%q native git=%q", got, want)
			}
			// Branch and remote must come from the same repository.
			url, err := exec.Command(binary, "-C", tc.path, "remote", "get-url", "origin").CombinedOutput()
			if err != nil {
				t.Fatalf("native git oracle: %v %s", err, url)
			}
			wantName := strings.TrimSuffix(filepath.Base(strings.TrimSpace(string(url))), ".git")
			if _, name := GetRemoteInfo(tc.path); name != wantName {
				t.Fatalf("GetRemoteInfo name=%q native git=%q", name, wantName)
			}
		})
	}
}
