package cloudshuttle

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

func TestPreserveManagedRewriteDoesNotRetainOldCursor(t *testing.T) {
	cfg := transactionConfig(t)
	cfg.Upload, cfg.IngestURL, cfg.DeviceKey = uploadAsymptote, "https://managed.invalid", "bcn_device_synthetic"
	transactionWrite(t, cfg.LogPath, transactionFirst)
	want := "{\"n\":8}\n{\"n\":9}\n"
	transactionClient(t, func(r *http.Request) (*http.Response, error) {
		if _, err := transactionBody(r); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(cfg.LogPath+".lock", os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		held, err := filelock.TryExclusive(f)
		if err != nil {
			return nil, err
		}
		defer held.Release()
		// WriteFile truncates the same inode; size growth alone cannot prove append.
		if err := os.WriteFile(cfg.LogPath, []byte(want), 0600); err != nil {
			return nil, err
		}
		return transactionOK(), nil
	})
	if err := preserveExistingLog(cfg); err == nil {
		t.Error("rewritten source reported successful reset")
	}
	contents, err := os.ReadFile(cfg.LogPath)
	if err != nil || string(contents) != want {
		t.Fatalf("rewritten source lost: %q %v", contents, err)
	}
	st, err := readState(cfg.StatePath)
	if err == nil && st.LastSize != 0 {
		t.Fatalf("acknowledged prefix does not belong to rewritten source: %+v", st)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestUploadCancellationWhileWaitingForWriterLock(t *testing.T) {
	cfg := transactionConfig(t)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	f, err := os.OpenFile(cfg.LogPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	held, err := filelock.Exclusive(f)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	var calls atomic.Int32
	transactionClient(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return transactionOK(), nil })
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Upload(ctx, cfg, true) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("writer contention = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("writer contention ignored context deadline")
		if err := held.Release(); err != nil {
			t.Fatal(err)
		}
		transactionAwait(t, done)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled snapshot touched provider")
	}
}

func TestUploadTransactionLockErrorsFailClosed(t *testing.T) {
	cfg := transactionConfig(t)
	transactionWrite(t, cfg.LogPath, transactionFirst)
	if err := os.Mkdir(cfg.StatePath+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	transactionClient(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return transactionOK(), nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := Upload(ctx, cfg, true)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("real lock-open error ignored/retried: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("lock-open error touched provider")
	}
	if err := os.Remove(cfg.StatePath + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := Upload(ctx, cfg, true); err != nil {
		t.Fatalf("retry after lock-open repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.StatePath), "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPreserveManagedAppendKeepsOwnedCursor(t *testing.T) {
	cfg := transactionConfig(t)
	cfg.Upload, cfg.IngestURL, cfg.DeviceKey = uploadAsymptote, "https://managed.invalid", "bcn_device_synthetic"
	transactionWrite(t, cfg.LogPath, transactionFirst)
	transactionClient(t, func(r *http.Request) (*http.Response, error) {
		if _, err := transactionBody(r); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(cfg.LogPath+".lock", os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		held, err := filelock.TryExclusive(f)
		if err != nil {
			return nil, err
		}
		defer held.Release()
		log, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if _, err := log.WriteString("{\"n\":2}\n"); err != nil {
			log.Close()
			return nil, err
		}
		if err := log.Close(); err != nil {
			return nil, err
		}
		return transactionOK(), nil
	})
	if err := preserveExistingLog(cfg); err == nil {
		t.Error("append during preservation reported successful reset")
	}
	contents, err := os.ReadFile(cfg.LogPath)
	if err != nil || string(contents) != transactionBoth {
		t.Fatalf("appended source lost: %q %v", contents, err)
	}
	st, err := readState(cfg.StatePath)
	if err != nil || st.LastSize != int64(len(transactionFirst)) {
		t.Fatalf("owned acknowledged prefix discarded: %+v %v", st, err)
	}
}

// Preserved or coarse timestamps cannot prove that the uploaded bytes still own the source.
func TestPreserveRechecksBytesWhenMetadataIsUnchanged(t *testing.T) {
	for _, target := range []string{uploadS3, uploadAsymptote} {
		for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			t.Run(target+"/"+http.StatusText(status), func(t *testing.T) {
				cfg := transactionConfig(t)
				if target == uploadAsymptote {
					cfg.Upload, cfg.IngestURL, cfg.DeviceKey = target, "https://managed.invalid", "bcn_device_synthetic"
				}
				transactionSeed(t, cfg)
				transactionWrite(t, cfg.LogPath, transactionFirst)
				before, err := os.Stat(cfg.LogPath)
				if err != nil {
					t.Fatal(err)
				}
				const replacement = "{\"n\":9}\n"
				transactionClient(t, func(r *http.Request) (*http.Response, error) {
					if _, err := transactionBody(r); err != nil {
						return nil, err
					}
					f, err := os.OpenFile(cfg.LogPath+".lock", os.O_RDWR, 0600)
					if err != nil {
						return nil, err
					}
					defer f.Close()
					held, err := filelock.TryExclusive(f)
					if err != nil {
						return nil, err
					}
					defer held.Release()
					if err := os.WriteFile(cfg.LogPath, []byte(replacement), 0600); err != nil {
						return nil, err
					}
					if err := os.Chtimes(cfg.LogPath, before.ModTime(), before.ModTime()); err != nil {
						return nil, err
					}
					after, err := os.Stat(cfg.LogPath)
					if err != nil {
						return nil, err
					}
					if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
						t.Error("rewrite control did not preserve inode, size and mtime")
					}
					resp := transactionOK()
					resp.StatusCode = status
					return resp, nil
				})
				if err := preserveExistingLog(cfg); err == nil {
					t.Error("different bytes with unchanged metadata reported successful preservation")
				}
				contents, err := os.ReadFile(cfg.LogPath)
				if err != nil || string(contents) != replacement {
					t.Fatalf("unseen replacement lost: %q %v", contents, err)
				}
				if cfg.Upload == uploadAsymptote {
					st, err := readState(cfg.StatePath)
					if err == nil && st.LastSize != 0 {
						t.Fatalf("old bytes acknowledged against replacement: %+v", st)
					}
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

// Exercise the finalization boundary with a real snapshot and a writer-locked append.
// Empty snapshots and failed providers do not establish a newer acknowledged cursor.
func TestPreserveRetainedSourceBoundsManagedCursorBySnapshot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		source    string
		lastSize  int64
		uploaded  bool
		wantOwned bool
	}{
		{name: "empty snapshot with old acknowledgment", lastSize: int64(len(transactionFirst)), uploaded: true},
		{name: "failed upload with oversized acknowledgment", source: transactionFirst, lastSize: int64(len(transactionBoth))},
		{name: "failed upload with owned acknowledgment", source: transactionFirst, lastSize: int64(len(transactionFirst)), wantOwned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := transactionConfig(t)
			cfg.Upload, cfg.IngestURL, cfg.DeviceKey = uploadAsymptote, "https://managed.invalid", "bcn_device_synthetic"
			transactionWrite(t, cfg.LogPath, tc.source)
			if err := writeState(cfg.StatePath, state{Provider: cfg.Provider, RunID: cfg.RunID,
				LastObject: cfg.IngestURL + ingestRuntimePath, LastSize: tc.lastSize}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release, err := lockUpload(ctx, cfg.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			snapshot, cleanup, err := snapshotLog(ctx, cfg.LogPath)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if snapshot.info.Size() != int64(len(tc.source)) {
				t.Fatal("snapshot length control failed")
			}
			f, err := os.OpenFile(cfg.LogPath+".lock", os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			held, err := filelock.TryExclusive(f)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			log, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := log.WriteString(transactionFirst); err != nil {
				log.Close()
				t.Fatal(err)
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			if err := held.Release(); err != nil {
				t.Fatal(err)
			}
			if err := finishPreservation(ctx, cfg, snapshot, tc.uploaded); err == nil {
				t.Error("append during finalization reported successful reset")
			}
			contents, err := os.ReadFile(cfg.LogPath)
			if err != nil || string(contents) != tc.source+transactionFirst {
				t.Fatalf("retained appended source lost: %q %v", contents, err)
			}
			st, err := readState(cfg.StatePath)
			if tc.wantOwned {
				if err != nil || st.LastSize != tc.lastSize {
					t.Fatalf("owned acknowledged prefix discarded: %+v %v", st, err)
				}
			} else {
				if err == nil && st.LastSize != 0 {
					t.Fatalf("acknowledgment beyond captured snapshot retained: snapshot=%d state=%+v", snapshot.info.Size(), st)
				}
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		})
	}
}
