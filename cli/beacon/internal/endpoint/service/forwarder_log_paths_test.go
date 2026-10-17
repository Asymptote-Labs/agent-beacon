package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestForwarderLogPathsCurrent(t *testing.T) {
	testenv.SetHome(t, filepath.Join(t.TempDir(), "home&custom"))
	pointSystemLogDir(t)
	for _, user := range []bool{true, false} {
		name := "system"
		if user {
			name = "user"
		}
		t.Run(name, func(t *testing.T) {
			stdout, stderr, err := launchdLogPaths(user, ForwarderLabel)
			if err != nil {
				t.Fatal(err)
			}
			current := forwarderPlist(ForwarderLabel, "/custom/vector", "/custom/config", stdout, stderr)
			cases := []struct {
				name, data   string
				current, bad bool
			}{
				{"current", current, true, false},
				{"custom arguments and formatting", strings.ReplaceAll(strings.ReplaceAll(current, "/custom/vector", "/operator/vector"), "  ", "    "), true, false},
				{"legacy", forwarderPlist(ForwarderLabel, "/custom/vector", "/custom/config", "/tmp/old.out", "/tmp/old.err"), false, false},
				{"only stderr legacy", forwarderPlist(ForwarderLabel, "/custom/vector", "/custom/config", stdout, "/tmp/old.err"), false, false},
				{"missing", `<plist><dict><key>Label</key><string>forwarder</string></dict></plist>`, false, false},
				{"nested paths are not job paths", `<plist><dict><key>Extra</key><dict><key>StandardOutPath</key><string>` + xmlEscape(stdout) + `</string><key>StandardErrorPath</key><string>` + xmlEscape(stderr) + `</string></dict></dict></plist>`, false, false},
				{"duplicate", strings.Replace(current, "</dict>", "<key>StandardOutPath</key><string>"+xmlEscape(stdout)+"</string></dict>", 1), false, true},
				{"non-string", strings.Replace(current, "<string>"+xmlEscape(stdout)+"</string>", "<array/>", 1), false, true},
				{"malformed", `<plist><dict>`, false, true},
				{"second root", current + "<other/>", false, true},
				{"leading text before declaration", "junk" + current, false, true},
				{"leading text before root", "junk<plist><dict/></plist>", false, true},
				{"leading whitespace and comment", " \n<!-- header -->\n" + strings.TrimPrefix(current, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"), true, false},
				{"trailing text", current + "junk", false, true},
				{"trailing whitespace and comment", current + "\n <!-- note -->\n", true, false},
				{"not a plist", `<dict/>`, false, true},
				{"multiple dictionaries", `<plist><dict/><dict/></plist>`, false, true},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					got, err := forwarderLogPathsCurrent([]byte(c.data), stdout, stderr)
					if got != c.current || (err != nil) != c.bad {
						t.Fatalf("current=%v err=%v; want current=%v error=%v", got, err, c.current, c.bad)
					}
				})
			}
		})
	}
}

func TestForwarderManagerChecksInstalledUserLogPathsWithoutWriting(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	manager := ForwarderManager{UserMode: true, Kind: KindLaunchd}
	path, err := manager.UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := launchdLogPaths(true, ForwarderLabel)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(forwarderPlist(ForwarderLabel, "/custom/vector", "/custom/config", stdout, stderr))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.LogPathsCurrent()
	if err != nil || !got {
		t.Fatalf("current=%v err=%v", got, err)
	}
	after, err := os.Stat(path)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("checker changed file: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LogPathsCurrent(); err == nil {
		t.Fatal("missing unit was treated as current")
	}
}

func TestForwarderManagerLeavesOtherBackendsAlone(t *testing.T) {
	for _, kind := range []Kind{KindSystemd, KindSupervised} {
		current, err := (ForwarderManager{Kind: kind}).LogPathsCurrent()
		if err != nil || !current {
			t.Fatalf("kind=%s current=%v err=%v", kind, current, err)
		}
	}
}
