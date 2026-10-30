package diagnostics

import (
	"errors"
	"io/fs"
	"testing"
)

// checkRestrictedLogACL is doctor's question about a restricted log on Windows, answered here from
// a stand-in for the ACL reader so every outcome is checked on every platform.
func TestCheckRestrictedLogACL(t *testing.T) {
	cases := []struct {
		name     string
		problems []string
		err      error
		status   string
		evidence string
	}{
		{"as install leaves it", nil, nil, StatusOK, "restricted_acl"},
		{"a leftover grant", []string{`runtime.jsonl: grants S-1-5-32-545 access (0x1200a9)`}, nil, StatusFail, "restricted_log_too_open"},
		{"run without elevation", nil, &fs.PathError{Op: "open", Path: "logs", Err: fs.ErrPermission}, StatusOK, "restricted_log_not_readable_by_caller"},
		{"too open, then refused", []string{`logs: grants S-1-5-32-545 access (0x120089)`}, &fs.PathError{Op: "open", Path: "logs", Err: fs.ErrPermission}, StatusFail, "restricted_log_too_open"},
		{"no directory", nil, &fs.PathError{Op: "open", Path: "logs", Err: fs.ErrNotExist}, StatusFail, "log_dir_missing"},
		{"the readers group is gone", nil, errors.New("the beacon-log group does not exist"), StatusWarn, "acl_unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := restrictedLogProblems
			restrictedLogProblems = func(string) ([]string, error) { return tc.problems, tc.err }
			t.Cleanup(func() { restrictedLogProblems = prev })
			c := checkRestrictedLogACL(`C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl`)
			if c.Status != tc.status || c.Evidence != tc.evidence {
				t.Fatalf("check = %+v, want %s/%s", c, tc.status, tc.evidence)
			}
			if c.Status == StatusFail && c.Action == "" {
				t.Fatal("a failing check names no repair")
			}
		})
	}
}
