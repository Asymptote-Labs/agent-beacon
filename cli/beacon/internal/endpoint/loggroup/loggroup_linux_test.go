//go:build linux

package loggroup

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type fakeSystem struct {
	groups  map[string]string // name -> getent line
	calls   []string
	failAdd error
	tools   map[string]bool
}

// exitCode returns a real *exec.ExitError with the given code, the shape run returns.
func exitCode(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+string(rune('0'+code))).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("could not produce an exit error: %v", err)
	}
	return err
}

func install(t *testing.T, sys *fakeSystem) {
	t.Helper()
	prevRun, prevLook := run, lookPath
	t.Cleanup(func() { run, lookPath = prevRun, prevLook })
	lookPath = func(candidates ...string) (string, bool) {
		for _, c := range candidates {
			if sys.tools[c] {
				return "/usr/sbin/" + c, true
			}
		}
		return "", false
	}
	run = func(path string, args ...string) (string, error) {
		call := path + " " + strings.Join(args, " ")
		sys.calls = append(sys.calls, call)
		switch {
		case strings.HasSuffix(path, "getent"):
			if line, ok := sys.groups[args[1]]; ok {
				return line + "\n", nil
			}
			return "", exitCode(t, 2)
		case strings.HasSuffix(path, "groupadd"), strings.HasSuffix(path, "addgroup"):
			if sys.failAdd != nil {
				return "", sys.failAdd
			}
			name := args[len(args)-1]
			sys.groups[name] = name + ":x:998:"
			return "", nil
		}
		t.Fatalf("unexpected command %s", call)
		return "", nil
	}
}

func TestEnsureFindsAnExistingGroup(t *testing.T) {
	sys := &fakeSystem{groups: map[string]string{"beacon-log": "beacon-log:x:1234:splunk,fluentd"}, tools: map[string]bool{"getent": true, "groupadd": true}}
	install(t, sys)
	gid, created, err := Ensure("beacon-log")
	if err != nil || gid != 1234 || created {
		t.Fatalf("Ensure = %d, %v, %v", gid, created, err)
	}
	for _, call := range sys.calls {
		if strings.Contains(call, "groupadd") {
			t.Fatal("created a group that already existed")
		}
	}
}

func TestEnsureCreatesASystemGroup(t *testing.T) {
	sys := &fakeSystem{groups: map[string]string{}, tools: map[string]bool{"getent": true, "groupadd": true}}
	install(t, sys)
	gid, created, err := Ensure("beacon-log")
	if err != nil || gid != 998 || !created {
		t.Fatalf("Ensure = %d, %v, %v", gid, created, err)
	}
	if !contains(sys.calls, "/usr/sbin/groupadd --system beacon-log") {
		t.Fatalf("calls = %v", sys.calls)
	}
}

func TestEnsureFallsBackToBusyboxAddgroup(t *testing.T) {
	sys := &fakeSystem{groups: map[string]string{}, tools: map[string]bool{"getent": true, "addgroup": true}}
	install(t, sys)
	if _, created, err := Ensure("beacon-log"); err != nil || !created {
		t.Fatalf("Ensure = %v, %v", created, err)
	}
	if !contains(sys.calls, "/usr/sbin/addgroup -S beacon-log") {
		t.Fatalf("calls = %v", sys.calls)
	}
}

func TestEnsureReportsAFailedCreate(t *testing.T) {
	sys := &fakeSystem{groups: map[string]string{}, tools: map[string]bool{"getent": true, "groupadd": true}, failAdd: errors.New("permission denied")}
	install(t, sys)
	if _, _, err := Ensure("beacon-log"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Ensure = %v", err)
	}
}

func TestEnsureWithoutTools(t *testing.T) {
	install(t, &fakeSystem{groups: map[string]string{}, tools: map[string]bool{"getent": true}})
	if _, _, err := Ensure("beacon-log"); err == nil {
		t.Fatal("Ensure succeeded with no way to create a group")
	}
	install(t, &fakeSystem{groups: map[string]string{}, tools: map[string]bool{}})
	if _, _, err := Lookup("beacon-log"); err == nil {
		t.Fatal("Lookup succeeded without getent")
	}
}

func TestLookupRejectsOutputForAnotherGroup(t *testing.T) {
	install(t, &fakeSystem{groups: map[string]string{"beacon-log": "other:x:5:"}, tools: map[string]bool{"getent": true}})
	if _, _, err := Lookup("beacon-log"); err == nil {
		t.Fatal("accepted getent output naming another group")
	}
}

func TestLookupRejectsAGarbledGID(t *testing.T) {
	install(t, &fakeSystem{groups: map[string]string{"beacon-log": "beacon-log:x:abc:"}, tools: map[string]bool{"getent": true}})
	if _, _, err := Lookup("beacon-log"); err == nil {
		t.Fatal("accepted a non-numeric gid")
	}
}

func TestInvalidNamesNeverReachATool(t *testing.T) {
	sys := &fakeSystem{groups: map[string]string{}, tools: map[string]bool{"getent": true, "groupadd": true}}
	install(t, sys)
	for _, name := range []string{"", "-rf", "a b", "a/b", "a;b", strings.Repeat("x", 33), "naïve"} {
		if _, _, err := Ensure(name); err == nil {
			t.Errorf("Ensure(%q) accepted", name)
		}
	}
	if len(sys.calls) != 0 {
		t.Fatalf("invalid names reached a tool: %v", sys.calls)
	}
}

// Against the real account database: root's group always exists, and a random name never does.
func TestLookupOnThisSystem(t *testing.T) {
	if _, err := exec.LookPath("getent"); err != nil {
		t.Skip("no getent")
	}
	if gid, found, err := Lookup("root"); err != nil || !found || gid != 0 {
		t.Fatalf("Lookup(root) = %d, %v, %v", gid, found, err)
	}
	if _, found, err := Lookup("beacon-no-such-group-x9"); err != nil || found {
		t.Fatalf("Lookup(missing) = %v, %v", found, err)
	}
}

func contains(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
