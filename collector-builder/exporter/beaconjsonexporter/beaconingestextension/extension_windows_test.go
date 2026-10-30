//go:build windows

package beaconingestextension

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/windows"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

func currentSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

type winFixture struct {
	ext    *ingest
	client localingest.Client
	log    string
	pipe   string
}

func startOnPipe(t *testing.T) *winFixture {
	t.Helper()
	cfg := createDefaultConfig()
	cfg.SocketPath = fmt.Sprintf(`\\.\pipe\beacon-ingest-ext-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	cfg.LogPath = filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	ext := newIngest(cfg, zap.NewNop())
	if err := ext.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ext.Shutdown(context.Background()) })
	return &winFixture{ext: ext, log: cfg.LogPath, pipe: cfg.SocketPath,
		client: localingest.Client{SocketPath: cfg.SocketPath, TrustedSID: currentSID(t), LogPath: cfg.LogPath, Timeout: 5 * time.Second}}
}

func winEvent(t *testing.T, i int) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{
		"timestamp":      fmt.Sprintf("2026-10-30T09:03:%02d.000000000Z", i%60),
		"vendor":         asymptoteobserve.Vendor,
		"product":        asymptoteobserve.Product,
		"schema_version": asymptoteobserve.SchemaVersion,
		"event":          map[string]interface{}{"action": "command.executed", "category": "command"},
		"session":        map[string]interface{}{"id": fmt.Sprintf("s-%d", i)},
		"user":           map[string]interface{}{"name": `NT AUTHORITY\SYSTEM`, "uid": "S-1-5-18"},
		"message":        fmt.Sprintf("event %d", i),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEventsSentToThePipeLandInTheLogUnderTheSendersAccount(t *testing.T) {
	f := startOnPipe(t)
	resp, err := f.client.Send([][]byte{winEvent(t, 1), winEvent(t, 2)})
	if err != nil || resp.Accepted != 2 {
		t.Fatalf("Send = %+v, %v", resp, err)
	}
	file, err := os.Open(f.log)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	sid := currentSID(t)
	me, _ := user.Current()
	var n int
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var e map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("log line is not one JSON object: %q", scanner.Text())
		}
		u := e["user"].(map[string]interface{})
		if u["uid"] != sid {
			t.Fatalf("user.uid = %v; the event claimed SYSTEM and must carry the sender's SID %s", u["uid"], sid)
		}
		if me != nil && u["name"] != me.Username {
			t.Fatalf("user.name = %v, want %q", u["name"], me.Username)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("log has %d lines, want 2", n)
	}
}

func TestASecondCollectorCannotServeTheSamePipe(t *testing.T) {
	f := startOnPipe(t)
	cfg := *f.ext.cfg
	second := newIngest(&cfg, zap.NewNop())
	if err := second.Start(context.Background(), nil); err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("a second collector started on a pipe another one serves")
	}
}

func TestShutdownLeavesNoPipe(t *testing.T) {
	f := startOnPipe(t)
	if err := f.client.Probe(); err != nil {
		t.Fatalf("Probe = %v", err)
	}
	if err := f.ext.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Probe(); !errors.Is(err, localingest.ErrNoSocket) {
		t.Fatalf("Probe after Shutdown = %v, want ErrNoSocket", err)
	}
}

func TestResolveNameNamesTheCurrentAccount(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	if got := resolveName(currentSID(t)); got != me.Username {
		t.Fatalf("resolveName = %q, want %q", got, me.Username)
	}
	if got := resolveName("S-1-5-21-1-2-3-999999"); got != "" {
		t.Fatalf("resolveName of an unknown SID = %q", got)
	}
}

// Accounts that are not SidTypeUser are named too. os/user refused them, which would have left
// events from SYSTEM, a service's virtual account or an Entra ID account without a name.
func TestResolveNameNamesAccountsThatAreNotLocalUsers(t *testing.T) {
	if got := resolveName("S-1-5-18"); got != `NT AUTHORITY\SYSTEM` {
		t.Fatalf("resolveName(SYSTEM) = %q", got)
	}
	if got := resolveName("not a sid"); got != "" {
		t.Fatalf("resolveName of a malformed SID = %q", got)
	}
}

func TestValidWindowsName(t *testing.T) {
	for _, ok := range []string{`CONTOSO\Jane Doe`, `DESKTOP-1\runneradmin`, `NT AUTHORITY\SYSTEM`} {
		if !validWindowsName(ok) {
			t.Errorf("validWindowsName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", " lead", "trail ", "nl\nx", "esc\x1b[31m", "del\x7f", string(make([]byte, 257))} {
		if validWindowsName(bad) {
			t.Errorf("validWindowsName(%q) = true", bad)
		}
	}
}

// A directory service that does not answer costs one connection the lookup timeout, not the
// client's whole budget: the event is named by SID only.
func TestResolveNameGivesUpOnASlowLookup(t *testing.T) {
	prev := lookupAccount
	release := make(chan struct{})
	lookupAccount = func(string) (string, error) {
		<-release
		return `CONTOSO\late`, nil
	}
	t.Cleanup(func() { close(release); lookupAccount = prev })
	start := time.Now()
	if got := resolveName("S-1-5-21-1-2-3-1001"); got != "" {
		t.Fatalf("resolveName = %q, want nothing past the timeout", got)
	}
	if elapsed := time.Since(start); elapsed > nameLookupTimeout+time.Second {
		t.Fatalf("resolveName took %v", elapsed)
	}
}
