package sentinel

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel/sentinelmock"
)

// The forwarder's real config, run by real Vector against a mock of Entra ID and the Logs Ingestion
// API. It proves what no unit test can: that Vector 0.56 accepts the config, authenticates, posts
// the rows the DCR expects, survives a stretch of 403s, follows a rotation, does not resend after a
// restart or after the restricted-mode switch replaces the log with a copy, and never loses a line.
//
// Needs BEACON_TEST_VECTOR_BIN. Vector's token client trusts only the operating system's store, so
// the mock's CA has to be trusted there: on Linux through SSL_CERT_FILE, which this test sets; on
// Windows by importing it into LocalMachine\Root, which this test does only when
// BEACON_TEST_SENTINEL_TRUST_STORE=1 (CI) because it changes the machine. macOS has no switch for it,
// so the test skips there.
func TestForwarderConfigDeliversThroughRealVector(t *testing.T) {
	vectorBin := os.Getenv("BEACON_TEST_VECTOR_BIN")
	if vectorBin == "" {
		t.Skip("set BEACON_TEST_VECTOR_BIN to run the forwarder against real Vector")
	}
	switch runtime.GOOS {
	case "darwin":
		t.Skip("Vector's token client on macOS trusts only the keychain; run this on Linux or Windows")
	case "windows":
		if os.Getenv("BEACON_TEST_SENTINEL_TRUST_STORE") != "1" {
			t.Skip("set BEACON_TEST_SENTINEL_TRUST_STORE=1 to let this test trust the mock's CA in LocalMachine\\Root")
		}
	}

	dir := t.TempDir()
	caFile := filepath.Join(dir, "mock-ca.pem")
	settings := Settings{
		Endpoint:       "",
		DCRImmutableID: "dcr-0123456789abcdef0123456789abcdef",
		TenantID:       "11111111-2222-3333-4444-555555555555",
		ClientID:       "66666666-7777-8888-9999-000000000000",
		Credential:     CredentialClientSecret,
		ReadFrom:       "beginning",
		LogPath:        filepath.Join(dir, "logs", "runtime.jsonl"),
	}
	mock, err := sentinelmock.Start(sentinelmock.Options{
		TenantID: settings.TenantID, ClientID: settings.ClientID, ClientSecret: "s3cret",
		DCRImmutableID: settings.DCRImmutableID, Stream: "Custom-BeaconRuntimeRaw", CAFile: caFile,
		// A role assignment that has not propagated yet.
		IngestStatuses: []int{403, 403, 403},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	settings.Endpoint = mock.URL
	trustCA(t, caFile)

	state := filepath.Join(dir, "forwarder")
	render := renderOptions{
		Settings:    settings,
		DataDir:     filepath.Join(state, dataDirName),
		SecretsPath: filepath.Join(state, secretsFileName),
		LogDir:      filepath.Join(state, logDirName),
	}
	if err := writeFiles(render, []byte(`{"client_secret":"s3cret"}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settings.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(state, configFileName)
	if err := os.WriteFile(configPath, []byte(renderForwarderConfig(render)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(vectorBin, "validate", "--skip-healthchecks", configPath).CombinedOutput(); err != nil {
		t.Fatalf("vector validate: %v\n%s", err, out)
	}

	log := &testLog{t: t, path: settings.LogPath}
	log.append(5) // written before Vector starts, like the install backfill

	vector := startVector(t, vectorBin, configPath, mock.URL, caFile)
	log.append(20)
	waitForRows(t, mock, log.ids, 90*time.Second)
	if _, _, rejected := mock.Counts(); rejected != 3 {
		t.Fatalf("the mock refused %d requests, want the 3 injected 403s to have been retried", rejected)
	}

	// A rotation: lines appended just before it land in runtime.jsonl.1.
	log.append(10)
	if err := os.Rename(settings.LogPath, settings.LogPath+".1"); err != nil {
		t.Fatal(err)
	}
	log.append(10)
	waitForRows(t, mock, log.ids, 60*time.Second)

	graceful := vector.stop()
	log.append(5) // written while Vector is down
	vector = startVector(t, vectorBin, configPath, mock.URL, caFile)
	log.append(5)
	waitForRows(t, mock, log.ids, 60*time.Second)

	// The restricted-mode switch replaces the log with a copy under the same name. Vector recognizes
	// a file by its first line, so it keeps reading the replaced one; install and repair, which make
	// the switch, then restart the forwarder (Reconcile with Restart), and it resumes in the copy.
	// On Windows a plain rename cannot replace a file Vector holds open (Beacon's writer renames with
	// POSIX semantics instead), so there the MSI smoke covers it, with the real switch.
	if runtime.GOOS != "windows" {
		data, err := os.ReadFile(settings.LogPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settings.LogPath+".copy", data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(settings.LogPath+".copy", settings.LogPath); err != nil {
			t.Fatal(err)
		}
		log.append(5) // between the switch and the restart
		if !vector.stop() {
			graceful = false
		}
		vector = startVector(t, vectorBin, configPath, mock.URL, caFile)
		log.append(5)
		waitForRows(t, mock, log.ids, 60*time.Second)
	}
	time.Sleep(3 * time.Second) // let anything resent arrive before counting
	vector.stop()

	seen := map[string]int{}
	for _, row := range mock.Rows() {
		var event struct {
			Event struct {
				ID string `json:"id"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(row.RawData), &event); err != nil {
			t.Fatalf("RawData is not the Beacon line: %v: %s", err, row.RawData)
		}
		if row.TimeGenerated == "" {
			t.Fatalf("a row has no TimeGenerated: %+v", row)
		}
		seen[event.Event.ID]++
	}
	for _, id := range log.ids {
		if seen[id] == 0 {
			t.Errorf("event %s never reached the mock", id)
		}
		// Vector saves read positions on a graceful stop. Without one (Windows has no console signal
		// to send it here) a resend is allowed; a loss never is.
		if seen[id] > 1 && graceful {
			t.Errorf("event %s was sent %d times", id, seen[id])
		}
	}
	if !t.Failed() {
		tokens, ingests, _ := mock.Counts()
		t.Logf("%d events delivered exactly once across retried 403s, a rotation, a restart and (off Windows) a log copy; %d token requests, %d ingest requests", len(log.ids), tokens, ingests)
	}
}

type testLog struct {
	t    *testing.T
	path string
	ids  []string
}

func (l *testLog) append(n int) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(l.ids))
		l.ids = append(l.ids, id)
		line, _ := json.Marshal(map[string]any{
			"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
			"vendor":         "Asymptote Labs",
			"product":        "Beacon Endpoint Agent",
			"schema_version": "1.0",
			"event":          map[string]any{"id": id, "action": "tool.invoked", "category": "agent"},
			"user":           map[string]any{"name": "tester", "uid": "1000"},
		})
		w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		l.t.Fatal(err)
	}
}

type vectorProcess struct {
	t   *testing.T
	cmd *exec.Cmd
	out *strings.Builder
}

func startVector(t *testing.T, bin, config, authority, caFile string) *vectorProcess {
	t.Helper()
	cmd := exec.Command(bin, "--config", config, "--graceful-shutdown-limit-secs", "10")
	cmd.Env = append(os.Environ(), "AZURE_AUTHORITY_HOST="+authority, "VECTOR_LOG=info")
	if runtime.GOOS == "linux" {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+caFile)
	}
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &vectorProcess{t: t, cmd: cmd, out: out}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if t.Failed() {
			t.Logf("vector output:\n%s", out.String())
		}
	})
	return p
}

// stop ends Vector, gracefully where the platform allows it, and reports whether it did.
func (p *vectorProcess) stop() bool {
	graceful := runtime.GOOS != "windows"
	if graceful {
		_ = p.cmd.Process.Signal(os.Interrupt)
	} else {
		_ = p.cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		graceful = false
	}
	return graceful
}

func waitForRows(t *testing.T, mock *sentinelmock.Server, ids []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		seen := map[string]bool{}
		for _, row := range mock.Rows() {
			for _, id := range ids {
				if strings.Contains(row.RawData, id) {
					seen[id] = true
				}
			}
		}
		if len(seen) == len(ids) {
			return
		}
		if time.Now().After(deadline) {
			tokens, ingests, rejected := mock.Counts()
			t.Fatalf("%d of %d events reached the mock in %s (token requests %d, ingest requests %d, refused %d)",
				len(seen), len(ids), timeout, tokens, ingests, rejected)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// trustCA puts the mock's CA where Vector's token client looks. Linux needs nothing here:
// startVector points SSL_CERT_FILE at it.
func trustCA(t *testing.T, caFile string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	if out, err := exec.Command("certutil", "-addstore", "-f", "Root", caFile).CombinedOutput(); err != nil {
		t.Fatalf("certutil -addstore Root: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		// The mock's CA has its own random name; delete exactly that certificate, by its SHA-1
		// thumbprint, which is how certutil names one.
		data, err := os.ReadFile(caFile)
		if err != nil {
			t.Logf("removing the mock CA: %v", err)
			return
		}
		block, _ := pem.Decode(data)
		if block == nil {
			t.Log("removing the mock CA: not PEM")
			return
		}
		thumbprint := sha1.Sum(block.Bytes)
		if out, err := exec.Command("certutil", "-delstore", "Root", hex.EncodeToString(thumbprint[:])).CombinedOutput(); err != nil {
			t.Logf("removing the mock CA: %v\n%s", err, out)
		}
	})
}
