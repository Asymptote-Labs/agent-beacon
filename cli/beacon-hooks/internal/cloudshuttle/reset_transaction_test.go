package cloudshuttle

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreserveSharesUploadTransaction(t *testing.T) {
	cfg := transactionConfig(t)
	transactionSeed(t, cfg)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	transactionClient(t, func(r *http.Request) (*http.Response, error) {
		if _, err := transactionBody(r); err != nil {
			return nil, err
		}
		if calls.Add(1) == 1 {
			close(first)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		} else {
			close(second)
		}
		return transactionOK(), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uploaded := make(chan error, 1)
	go func() { uploaded <- Upload(ctx, cfg, true) }()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	preserved := make(chan error, 1)
	go func() { preserved <- preserveExistingLog(cfg) }()
	select {
	case <-second:
		t.Error("preservation contacted provider while upload transaction still held")
	case <-time.After(150 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := transactionAwait(t, uploaded); err != nil {
		t.Error(err)
	}
	if err := transactionAwait(t, preserved); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(cfg.LogPath); !os.IsNotExist(err) {
		t.Fatalf("unchanged preserved source: %v", err)
	}
}

func TestPreserveManagedReplacementDoesNotAcknowledgeNewSource(t *testing.T) {
	cfg := transactionConfig(t)
	cfg.Upload, cfg.IngestURL, cfg.DeviceKey = uploadAsymptote, "https://managed.invalid", "bcn_device_synthetic"
	transactionSeed(t, cfg)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	transactionClient(t, func(r *http.Request) (*http.Response, error) {
		if _, err := transactionBody(r); err != nil {
			return nil, err
		}
		if err := os.Rename(cfg.LogPath, cfg.LogPath+".1"); err != nil {
			return nil, err
		}
		if err := os.WriteFile(cfg.LogPath, []byte("{\"n\":9}\n"), 0600); err != nil {
			return nil, err
		}
		return transactionOK(), nil
	})
	if err := preserveExistingLog(cfg); err == nil {
		t.Error("replacement during preservation reported success")
	}
	contents, err := os.ReadFile(cfg.LogPath)
	if err != nil || string(contents) != "{\"n\":9}\n" {
		t.Fatalf("new generation lost: %q %v", contents, err)
	}
	st, err := readState(cfg.StatePath)
	if err == nil && st.LastSize != 0 {
		t.Fatalf("old snapshot acknowledged as replacement: %+v", st)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestManagedConcurrentCursorDoesNotRewind(t *testing.T) {
	cfg := transactionConfig(t)
	cfg.Upload, cfg.IngestURL, cfg.DeviceKey = uploadAsymptote, "https://managed.invalid", "bcn_device_synthetic"
	transactionWrite(t, cfg.LogPath, transactionFirst)
	entered, newer, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	transactionClient(t, func(r *http.Request) (*http.Response, error) {
		if _, err := transactionBody(r); err != nil {
			return nil, err
		}
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		} else {
			close(newer)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(http.NoBody)}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := make(chan error, 1)
	go func() { a <- Upload(ctx, cfg, true) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	transactionWrite(t, cfg.LogPath, transactionBoth)
	b := make(chan error, 1)
	go func() { b <- Upload(ctx, cfg, true) }()
	finishedB := false
	select {
	case <-newer:
		if err := transactionAwait(t, b); err != nil {
			t.Error(err)
		}
		finishedB = true
	case <-time.After(150 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := transactionAwait(t, a); err != nil {
		t.Error(err)
	}
	if !finishedB {
		if err := transactionAwait(t, b); err != nil {
			t.Error(err)
		}
	}
	st, err := readState(cfg.StatePath)
	if err != nil || st.LastSize != int64(len(transactionBoth)) {
		t.Fatalf("managed cursor rewound: %+v %v", st, err)
	}
}
