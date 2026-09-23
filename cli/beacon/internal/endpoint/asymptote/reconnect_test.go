package asymptote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Issue #450: a re-connect exchanges the code first, and the server rotates the device key in
// place at that moment. Every step after the exchange that fails used to leave the previous
// enrollment record and vector.toml on disk, so status said "connected" while the running
// forwarder held the key the server had just rotated out. These tests pin that a failure
// after the exchange is always reported as an incomplete connect, never as connected.

var (
	originalDeviceKey = "bcn_device_abcdefgh_" + strings.Repeat("k", 43)
	rotatedDeviceKey  = "bcn_device_rotated1_" + strings.Repeat("r", 43)
)

const movedIngestHost = "moved.example.test"

// fakeVectorRejecting answers validate with exit 78 only for a config that mentions reject,
// so the pre-enrollment preflight (which renders the previous ingest URL) passes and the
// validate of the config carrying the server's new ingest URL fails.
func fakeVectorRejecting(t *testing.T, reject string) string {
	t.Helper()
	path := fakeVector(t, "0.56.0", 0)
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo \"vector 0.56.0 (aarch64-apple-darwin test)\";;\n" +
		"  validate) if grep -q '" + reject + "' \"$3\"; then echo 'ingest host refused' >&2; exit 78; fi; exit 0;;\n" +
		"  *) exit 0;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// connectedEndpoint performs a successful first connect and arranges for every later exchange
// to rotate the key, as the server does for a known install id.
func connectedEndpoint(t *testing.T, vector string) (*fakeDashboard, *fakeForwarder) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, vector)); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if !Connected(true) || ConnectIncomplete(true) {
		t.Fatal("a successful connect must be connected and not incomplete")
	}
	if key, _ := ReadDeviceKey(true); key != originalDeviceKey {
		t.Fatalf("first connect stored an unexpected key")
	}
	fd.mu.Lock()
	fd.deviceKey = rotatedDeviceKey
	fd.mu.Unlock()
	return fd, fwd
}

// assertConnectIncomplete is the contract after any failure that follows the exchange: the
// endpoint is not reported as connected, status says the connect did not finish and names the
// retry command, and no device key leaks into the error, the marker, or status JSON.
func assertConnectIncomplete(t *testing.T, connectErr error) {
	t.Helper()
	if connectErr == nil {
		t.Fatal("connect should have failed")
	}
	if Connected(true) {
		t.Fatal("an endpoint whose connect failed after the key exchange must not be reported as connected")
	}
	if !ConnectIncomplete(true) {
		t.Fatal("ConnectIncomplete must be true after a failure that followed the key exchange")
	}
	status := Status(true, StatusOptions{SkipCredentialCheck: true})
	if status.Enabled || !status.ConnectIncomplete {
		t.Fatalf("status must not report an incomplete connect as connected: %+v", status)
	}
	if !strings.Contains(status.Message, "incomplete") || !strings.Contains(status.Message, "beacon endpoint connect") {
		t.Fatalf("status message must say the connect is incomplete and name the retry command: %q", status.Message)
	}
	msg := connectErr.Error()
	if !strings.Contains(msg, "incomplete") || !strings.Contains(msg, "beacon endpoint connect") {
		t.Fatalf("connect error must say the connect is incomplete and name the retry command: %v", connectErr)
	}
	marker, err := os.ReadFile(ConnectPendingPath(true))
	if err != nil {
		t.Fatalf("pending marker: %v", err)
	}
	if info, _ := os.Stat(ConnectPendingPath(true)); info.Mode().Perm() != 0o600 {
		t.Fatalf("pending marker mode = %o, want 600", info.Mode().Perm())
	}
	for _, text := range []string{msg, string(marker), string(mustJSON(t, status))} {
		for _, key := range []string{originalDeviceKey, rotatedDeviceKey} {
			if strings.Contains(text, key) {
				t.Fatalf("a device key leaked outside the secrets file: %s", text)
			}
		}
	}
}

func TestReconnectThatCannotStoreTheRotatedKeyIsReportedIncomplete(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	// A directory where the secrets file belongs makes the atomic rename fail, the same way
	// a full or read-only disk would, after the server has already rotated the key.
	if err := os.Remove(SecretsPath(true)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(SecretsPath(true), "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)
	if !strings.Contains(err.Error(), "could not store the device key") {
		t.Fatalf("error should name the failed step: %v", err)
	}
	if fwd.loads != 1 {
		t.Fatalf("the forwarder must not be reloaded without a stored key: loads=%d", fwd.loads)
	}
}

func TestReconnectWhoseNewIngestURLVectorRejectsIsReportedIncomplete(t *testing.T) {
	vector := fakeVectorRejecting(t, movedIngestHost)
	fd, fwd := connectedEndpoint(t, vector)
	fd.mu.Lock()
	fd.ingestURL = "https://" + movedIngestHost
	fd.mu.Unlock()

	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, vector))
	assertConnectIncomplete(t, err)
	if !strings.Contains(err.Error(), "vector validate failed") {
		t.Fatalf("error should carry Vector's refusal: %v", err)
	}
	// This is exactly the state from the issue: the secrets file already holds the rotated
	// key, so a credential check alone would say "valid", while the running forwarder was
	// never restarted and still uploads with the key the server rotated out.
	if key, _ := ReadDeviceKey(true); key != rotatedDeviceKey {
		t.Fatal("the rotated key should be the one stored")
	}
	if fwd.loads != 1 || len(fwd.written) != 1 {
		t.Fatalf("a rejected config must not reach the service manager: loads=%d written=%d", fwd.loads, len(fwd.written))
	}
}

func TestReconnectThatCannotWriteTheForwarderUnitIsReportedIncomplete(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	fwd.writeErr = errors.New("unit directory is read-only")
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)
	if !strings.Contains(err.Error(), "unit directory is read-only") {
		t.Fatalf("error should carry the service manager's reason: %v", err)
	}
}

func TestReconnectThatCannotStartTheForwarderIsReportedIncomplete(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	fwd.loadErr = errors.New("bootstrap failed: 5: Input/output error")
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)
	if !strings.Contains(err.Error(), "could not be started") {
		t.Fatalf("error should keep the load failure wording: %v", err)
	}
}

func TestReconnectThatCannotRecordItsConfigIsReportedIncomplete(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	// config.json unreadable as a file: the last step, recording managed_ingest, fails.
	cfgPath := endpointconfig.ConfigPath(true)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfgPath, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)
}

func TestRetryAfterAnIncompleteReconnectReportsConnected(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	fwd.loadErr = errors.New("bootstrap failed")
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)

	fwd.loadErr = nil
	result, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !result.ReEnrolled || !Connected(true) || ConnectIncomplete(true) {
		t.Fatalf("a successful retry must be connected: connected=%t incomplete=%t", Connected(true), ConnectIncomplete(true))
	}
	if _, err := os.Stat(ConnectPendingPath(true)); !os.IsNotExist(err) {
		t.Fatalf("the pending marker must be removed once connect finishes: %v", err)
	}
	status := Status(true, StatusOptions{SkipCredentialCheck: true})
	if !status.Enabled || status.ConnectIncomplete || status.Message != "" {
		t.Fatalf("status after a successful retry = %+v", status)
	}
}

func TestCancelledReconnectLeavesAWorkingConnectionConnected(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	// The approval never completes: no key was issued, so nothing was rotated and the
	// running forwarder is still good.
	fd.mu.Lock()
	fd.exchangeFail = "enrollment session expired"
	fd.mu.Unlock()
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	if err == nil {
		t.Fatal("expected the exchange failure")
	}
	if strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("a connect that never received a key must not claim the connection is broken: %v", err)
	}
	if !Connected(true) || ConnectIncomplete(true) {
		t.Fatalf("a cancelled re-connect must leave the working connection connected: connected=%t incomplete=%t", Connected(true), ConnectIncomplete(true))
	}
	if key, _ := ReadDeviceKey(true); key != originalDeviceKey {
		t.Fatal("a cancelled re-connect must not touch the stored key")
	}
	if status := Status(true, StatusOptions{SkipCredentialCheck: true}); !status.Enabled || status.ConnectIncomplete {
		t.Fatalf("status after a cancelled re-connect = %+v", status)
	}
}

func TestCancelledRetryKeepsAnEarlierIncompleteReconnectFlagged(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	fwd.loadErr = errors.New("bootstrap failed")
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)

	// A later attempt that is cancelled before a key is issued changes nothing: the earlier
	// rotation still left the forwarder on a dead key.
	fwd.loadErr = nil
	fd.mu.Lock()
	fd.exchangeFail = "enrollment session expired"
	fd.mu.Unlock()
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))); err == nil {
		t.Fatal("expected the exchange failure")
	}
	if Connected(true) || !ConnectIncomplete(true) {
		t.Fatalf("an earlier incomplete re-connect must stay flagged: connected=%t incomplete=%t", Connected(true), ConnectIncomplete(true))
	}
}

func TestConnectRejectedBeforeTheExchangeIsNotFlaggedIncomplete(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	// The preflight fails before anything is sent to the server, so no key was rotated.
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 78))); err == nil {
		t.Fatal("expected the preflight to fail")
	}
	if !Connected(true) || ConnectIncomplete(true) {
		t.Fatalf("a preflight failure must leave the connection as it was: connected=%t incomplete=%t", Connected(true), ConnectIncomplete(true))
	}
}

func TestDisconnectClearsAnIncompleteConnect(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	fwd.loadErr = errors.New("bootstrap failed")
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	assertConnectIncomplete(t, err)

	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd, KeepCredentials: true}); err != nil {
		t.Fatal(err)
	}
	if ConnectIncomplete(true) {
		t.Fatal("disconnect --keep-credentials removed the forwarder, so there is no incomplete connect left to report")
	}
	status := Status(true, StatusOptions{SkipCredentialCheck: true})
	if status.Enabled || status.ConnectIncomplete || !strings.Contains(status.Message, "credentials for device dev-1 kept") {
		t.Fatalf("status after disconnect --keep-credentials = %+v", status)
	}
}

func TestFirstConnectFailingAfterApprovalIsReportedIncomplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true, loadErr: errors.New("bootstrap failed")}
	_, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0)))
	if err == nil || !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "beacon endpoint connect") {
		t.Fatalf("a first connect that fails after approval must say so and name the retry: %v", err)
	}
	if Connected(true) || !ConnectIncomplete(true) {
		t.Fatalf("connected=%t incomplete=%t", Connected(true), ConnectIncomplete(true))
	}
	status := Status(true, StatusOptions{SkipCredentialCheck: true})
	if status.Enabled || !status.ConnectIncomplete || !strings.Contains(status.Message, "beacon endpoint connect") {
		t.Fatalf("status after a first connect that failed after approval = %+v", status)
	}
	var encoded map[string]any
	if err := json.Unmarshal(mustJSON(t, status), &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["connect_incomplete"] != true {
		t.Fatalf("status JSON must expose connect_incomplete: %v", encoded)
	}
}

func TestReconnectAwaitingApprovalStillReportsTheWorkingConnection(t *testing.T) {
	fd, fwd := connectedEndpoint(t, fakeVector(t, "0.56.0", 0))
	// Until the server issues a key nothing has been rotated. A connect interrupted here
	// (Ctrl-C ends the process without unwinding) must leave no trace that flags the working
	// connection, so the state seen while the browser approval is pending is the final word.
	opts := connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))
	approve := opts.Enroll.OpenBrowser
	var connectedWhileWaiting, incompleteWhileWaiting bool
	opts.Enroll.OpenBrowser = func(url string) error {
		connectedWhileWaiting, incompleteWhileWaiting = Connected(true), ConnectIncomplete(true)
		return approve(url)
	}
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !connectedWhileWaiting || incompleteWhileWaiting {
		t.Fatalf("before a key is issued the endpoint must still read as connected: connected=%t incomplete=%t", connectedWhileWaiting, incompleteWhileWaiting)
	}
}
