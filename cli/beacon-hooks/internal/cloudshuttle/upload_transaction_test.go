package cloudshuttle

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

const transactionFirst = "{\"n\":1}\n"
const transactionBoth = transactionFirst + "{\"n\":2}\n"

type transactionTransport func(*http.Request) (*http.Response, error)

func (f transactionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func transactionConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{Upload: uploadS3, LogPath: filepath.Join(dir, "runtime.jsonl"),
		StatePath: filepath.Join(dir, "state.json"), Bucket: "synthetic-bucket", Provider: "claude_code_web",
		RunID: "same-run", S3Region: "us-east-1", S3AccessKeyID: "synthetic", S3SecretKey: "synthetic",
		S3Endpoint: "http://127.0.0.1:1"}
}

func transactionWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func transactionSeed(t *testing.T, cfg Config) {
	t.Helper()
	if err := writeState(cfg.StatePath, state{Provider: cfg.Provider, RunID: cfg.RunID,
		LastObject: "beacon/runtime/existing-run.jsonl.gz"}); err != nil {
		t.Fatal(err)
	}
}

func transactionBody(r *http.Request) (string, error) {
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	b, err := io.ReadAll(gz)
	return string(b), err
}

func transactionOK() *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}
}

func transactionClient(t *testing.T, transport transactionTransport) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: transport}
	t.Cleanup(func() { httpClient = old })
}

func transactionAwait(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("upload did not finish")
		return nil
	}
}

// Both successful uploads must commit in snapshot order. Cross-process exclusion is
// proven by TestUploadTransactionCrossProcessContentionAndIndependentTargets.
// The seed fixes the remote key independently of the wall clock.
func TestUploadSerializesSnapshotsAndCheckpoint(t *testing.T) {
	cfg := transactionConfig(t)
	transactionSeed(t, cfg)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	first, newer, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var mu sync.Mutex
	var remote string
	var requests int
	transactionClient(t, func(r *http.Request) (*http.Response, error) {
		body, err := transactionBody(r)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			close(first)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		} else {
			close(newer)
		}
		mu.Lock()
		remote = body
		mu.Unlock()
		return transactionOK(), nil
	})
	start := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done <- Upload(ctx, cfg, true)
		}()
		return done
	}
	a := start()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first upload never reached provider")
	}
	transactionWrite(t, cfg.LogPath, transactionBoth)
	b := start()
	// A broken transaction reaches/finishes the second PUT first; the correct
	// implementation waits until A releases its transaction ownership.
	select {
	case <-newer:
		if err := transactionAwait(t, b); err != nil {
			t.Error(err)
		}
		once.Do(func() { close(release) })
	case <-time.After(50 * time.Millisecond):
		once.Do(func() { close(release) })
		if err := transactionAwait(t, b); err != nil {
			t.Error(err)
		}
	}
	if err := transactionAwait(t, a); err != nil {
		t.Error(err)
	}
	mu.Lock()
	got := remote
	mu.Unlock()
	st, err := readState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != transactionBoth || st.LastSize != int64(len(transactionBoth)) {
		t.Fatalf("acknowledged newer snapshot lost: remote=%q LastSize=%d; want %q/%d", got, st.LastSize, transactionBoth, len(transactionBoth))
	}
	if st.LastObject != "beacon/runtime/existing-run.jsonl.gz" {
		t.Fatalf("existing bucket key changed: %+v", st)
	}
}

// transactionChild runs one Upload in a separate process with a short deadline; it must
// fail with context.DeadlineExceeded.
func transactionChild(t *testing.T, cfg Config) error {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "-test.run=^TestUploadTransactionChild$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "BEACON_TRANSACTION_CHILD="+string(data))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("child: %w\n%s", err, output)
	}
	return nil
}

func TestUploadTransactionChild(t *testing.T) {
	raw := os.Getenv("BEACON_TRANSACTION_CHILD")
	if raw == "" {
		return
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Upload(ctx, cfg, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended upload = %v; want deadline", err)
	}
}

// Holding the protocol lock from another process is a causal exclusion proof:
// the child must exhaust its deadline without touching the fake provider.
func TestUploadTransactionCrossProcessContentionAndIndependentTargets(t *testing.T) {
	cfg := transactionConfig(t)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	cfg.S3Endpoint = server.URL
	f, err := os.OpenFile(cfg.StatePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	held, err := filelock.Exclusive(f)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := transactionChild(t, cfg); err != nil {
		t.Error(err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("contended child touched provider %d times", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	other := cfg
	other.Bucket = "independent-bucket"
	other.StatePath = filepath.Join(filepath.Dir(cfg.StatePath), "independent.json")
	if err := Upload(ctx, other, true); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := Upload(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
}

func TestUploadReleasesTransactionAfterProviderAndCheckpointFailures(t *testing.T) {
	for _, fault := range []string{"rejection", "timeout", "checkpoint"} {
		t.Run(fault, func(t *testing.T) {
			cfg := transactionConfig(t)
			transactionWrite(t, cfg.LogPath, transactionFirst)
			if fault == "checkpoint" {
				if err := os.Mkdir(cfg.StatePath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			fail := true
			transactionClient(t, func(r *http.Request) (*http.Response, error) {
				if fail && fault == "timeout" {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				resp := transactionOK()
				if fail && fault == "rejection" {
					resp.StatusCode = 503
				}
				return resp, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err := Upload(ctx, cfg, true)
			cancel()
			if err == nil {
				t.Fatal("failed provider/checkpoint reported success")
			}
			if fault == "checkpoint" {
				if err := os.Remove(cfg.StatePath); err != nil {
					t.Fatal(err)
				}
			}
			fail = false
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := Upload(ctx, cfg, true); err != nil {
				t.Fatalf("retry blocked or failed after %s: %v", fault, err)
			}
			st, err := readState(cfg.StatePath)
			if err != nil || st.LastSize != int64(len(transactionFirst)) {
				t.Fatalf("retry checkpoint: %+v %v", st, err)
			}
			matches, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.StatePath), ".shuttle-state-*"))
			if len(matches) != 0 {
				t.Fatalf("checkpoint temporary files leaked: %v", matches)
			}
		})
	}
}
