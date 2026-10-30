//go:build windows

package localingest

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// systemLog is the log the Windows deliverer tests write; the pipe servers accept any.
const systemLog = `C:\ProgramData\Beacon\Endpoint\logs\runtime.jsonl`

// winLog stands in for the writer's own append: it records lines, or refuses them as a restricted
// log's ACL refuses an ordinary account.
type winLog struct {
	mu     sync.Mutex
	lines  []string
	refuse bool
}

func (l *winLog) append(line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refuse {
		return &fs.PathError{Op: "open", Path: systemLog, Err: windows.ERROR_ACCESS_DENIED}
	}
	l.lines = append(l.lines, string(line))
	return nil
}

func (l *winLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

// pipeDeliverer writes as an ordinary account to the pipe at path.
func pipeDeliverer(t *testing.T, path string) Deliverer {
	t.Helper()
	lastSendFailure.Store(0)
	t.Cleanup(func() { lastSendFailure.Store(0) })
	return Deliverer{
		Client:         Client{SocketPath: path, TrustedSID: currentSID(t), Timeout: 5 * time.Second},
		Spool:          Spool{Dir: filepath.Join(t.TempDir(), "spool")},
		Euid:           func() int { return 1000 },
		FailureBackoff: -1,
	}
}

func TestWindowsWriteGoesThroughThePipe(t *testing.T) {
	h := startPipeServer(t, nil)
	d := pipeDeliverer(t, h.path)
	log := &winLog{}
	outcome, err := d.WriteOutcome(systemLog, lines(t, 1)[0], log.append)
	if err != nil || outcome != Sent {
		t.Fatalf("WriteOutcome = %v, %v", outcome, err)
	}
	if h.rec.count() != 1 || log.count() != 0 {
		t.Fatalf("collector %d, direct %d", h.rec.count(), log.count())
	}
	if uid := decode(t, h.rec.lines[0])["user"].(map[string]interface{})["uid"]; uid != currentSID(t) {
		t.Fatalf("stamped uid = %v", uid)
	}
}

func TestWindowsWriteWithoutAPipeAppendsDirectly(t *testing.T) {
	d := pipeDeliverer(t, testPipe(t))
	log := &winLog{}
	outcome, err := d.WriteOutcome(systemLog, lines(t, 1)[0], log.append)
	if err != nil || outcome != Appended || log.count() != 1 {
		t.Fatalf("WriteOutcome = %v, %v; direct %d", outcome, err, log.count())
	}
}

// A restricted log refuses the direct append while the collector is down: the event is spooled,
// and reaches the collector with the next event once it is back.
func TestWindowsWriteSpoolsWhileTheCollectorIsDownAndDrainsAfter(t *testing.T) {
	path := testPipe(t)
	d := pipeDeliverer(t, path)
	log := &winLog{refuse: true}
	outcome, err := d.WriteOutcome(systemLog, lines(t, 1)[0], log.append)
	if err != nil || outcome != Spooled {
		t.Fatalf("WriteOutcome = %v, %v; want it spooled", outcome, err)
	}
	if n, _ := d.Pending(systemLog); n != 1 {
		t.Fatalf("Pending = %d", n)
	}
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s := &Server{Append: rec.append}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	if err := d.Write(systemLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 2 {
		t.Fatalf("collector received %d lines, want the new one and the spooled one", rec.count())
	}
	if n, _ := d.Pending(systemLog); n != 0 {
		t.Fatalf("Pending after recovery = %d", n)
	}
}

// What a restricted log's ACL actually returns to an ordinary account must read as a refusal for
// permission, which is the one error that spools.
func TestARealAccessDeniedIsAPermissionRefusal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", file, "/deny", "*"+currentSID(t)+":(W)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = exec.Command("icacls", file, "/reset").Run() })
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_ = f.Close()
		t.Fatal("opened a file this account is denied writing")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("open = %v; want it to read as fs.ErrPermission", err)
	}
}

// SYSTEM and administrators with Administrators enabled write the restricted log directly;
// everyone else, elevated for another group or not elevated at all, goes through the pipe.
func TestWindowsPrivilegeDecidesTheRoute(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	groups, err := token.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	adminEnabled := false
	for _, g := range groups.AllGroups() {
		if g.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && g.Attributes&windows.SE_GROUP_ENABLED != 0 {
			adminEnabled = true
		}
	}
	if processPrivileged() != adminEnabled {
		t.Fatalf("processPrivileged = %v, but Administrators enabled in this token = %v", processPrivileged(), adminEnabled)
	}
	if (Deliverer{}).Applies(systemLog) == processPrivileged() {
		t.Fatalf("Applies = %v while processPrivileged = %v", (Deliverer{}).Applies(systemLog), processPrivileged())
	}
}
