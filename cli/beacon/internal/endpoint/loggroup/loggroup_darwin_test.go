//go:build darwin

package loggroup

import (
	"errors"
	"strings"
	"testing"
)

func fakeDirectory(t *testing.T, groups map[string]int) *[]string {
	t.Helper()
	var calls []string
	prev := run
	t.Cleanup(func() { run = prev })
	run = func(path string, args ...string) (string, error) {
		calls = append(calls, path+" "+strings.Join(args, " "))
		switch path {
		case dscl:
			name := strings.TrimPrefix(args[2], "/Groups/")
			gid, ok := groups[name]
			if !ok {
				return "", errors.New("dscl: <dscl_cmd> DS Error: -14136 (eDSRecordNotFound)")
			}
			return "PrimaryGroupID: " + itoa(gid) + "\n", nil
		case dseditgroup:
			groups[args[len(args)-1]] = 401
			return "", nil
		}
		t.Fatalf("unexpected command %s %v", path, args)
		return "", nil
	}
	return &calls
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestDarwinEnsureCreatesTheGroup(t *testing.T) {
	calls := fakeDirectory(t, map[string]int{})
	gid, created, err := Ensure("beacon-log")
	if err != nil || gid != 401 || !created {
		t.Fatalf("Ensure = %d, %v, %v", gid, created, err)
	}
	want := dseditgroup + " -o create -r Beacon log readers beacon-log"
	found := false
	for _, c := range *calls {
		found = found || c == want
	}
	if !found {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestDarwinEnsureFindsTheGroup(t *testing.T) {
	calls := fakeDirectory(t, map[string]int{"beacon-log": 502})
	gid, created, err := Ensure("beacon-log")
	if err != nil || gid != 502 || created || len(*calls) != 1 {
		t.Fatalf("Ensure = %d, %v, %v, calls %v", gid, created, err, *calls)
	}
}

func TestDarwinLookupOnThisSystem(t *testing.T) {
	if gid, found, err := Lookup("wheel"); err != nil || !found || gid != 0 {
		t.Fatalf("Lookup(wheel) = %d, %v, %v", gid, found, err)
	}
	if _, found, err := Lookup("beacon-no-such-group-x9"); err != nil || found {
		t.Fatalf("Lookup(missing) = %v, %v", found, err)
	}
}
