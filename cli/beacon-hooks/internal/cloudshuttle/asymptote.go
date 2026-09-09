package cloudshuttle

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Asymptote managed ingest as a cloud upload target.
//
// A cloud sandbox cannot enroll interactively, so it is configured with a per-device key an
// organization admin minted for it (BEACON_CLOUD_DEVICE_KEY) and the ingest URL
// (BEACON_CLOUD_INGEST_URL), the same wire contract the laptops' Vector forwarders use:
// gzip NDJSON POSTed to /v1/ingest/runtime with the key as a bearer token. Unlike the bucket
// targets, which re-upload the whole log under one object name per run, this target sends
// only the lines appended since the last successful upload, in batches under the ingest
// limits, and remembers the offset in the shuttle state. A revoked key (401) or a rejected
// batch leaves the offset where it was, so the next hook retries the same lines.
const (
	uploadAsymptote     = "asymptote"
	ingestRuntimePath   = "/v1/ingest/runtime"
	deviceKeyPrefix     = "bcn_device_"
	asymptoteUserAgent  = "beacon-hooks-cloud"
	maxIngestBatchLines = 5000
	maxIngestBatchBytes = 4 << 20 // uncompressed; the service caps compressed bodies at 8 MiB
)

// asymptoteBatchLines is a variable so tests can force several batches from a small log.
var asymptoteBatchLines = maxIngestBatchLines

// IsSecureIngestURL mirrors the CLI's rule for where a device key may be sent: https
// anywhere, plain http only to loopback for local development.
func IsSecureIngestURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	}
	return false
}

func asymptoteConfigured(cfg Config) bool {
	return IsSecureIngestURL(cfg.IngestURL) && strings.HasPrefix(strings.TrimSpace(cfg.DeviceKey), deviceKeyPrefix)
}

// uploadAsymptoteIncremental sends the log's new lines to managed ingest and updates the
// shuttle state. `snapshot` is a consistent copy of the log; `size` its length.
func uploadAsymptoteIncremental(ctx context.Context, cfg Config, snapshot string, size int64) error {
	offset := int64(0)
	if st, err := readState(cfg.StatePath); err == nil && st.Provider == cfg.Provider && st.RunID == cfg.RunID && st.LastSize > 0 && st.LastSize <= size {
		offset = st.LastSize
	}
	if offset >= size {
		return nil
	}
	file, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	reader := bufio.NewReaderSize(file, 1<<20)
	sent := offset
	var batch bytes.Buffer
	lines := 0
	flush := func() error {
		if lines == 0 {
			return nil
		}
		if err := postIngestBatch(ctx, cfg, batch.Bytes()); err != nil {
			return err
		}
		sent += int64(batch.Len())
		batch.Reset()
		lines = 0
		return writeState(cfg.StatePath, state{
			LastUpload: time.Now().UTC().Format(time.RFC3339),
			LastSize:   sent,
			LastObject: cfg.IngestURL + ingestRuntimePath,
			Provider:   cfg.Provider,
			RunID:      cfg.RunID,
		})
	}
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			// Only complete lines are shipped; a partial trailing line waits for the next hook.
			batch.Write(line)
			lines++
			if lines >= asymptoteBatchLines || batch.Len() >= maxIngestBatchBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	return flush()
}

func postIngestBatch(ctx context.Context, cfg Config, ndjson []byte) error {
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	if _, err := gz.Write(ndjson); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.IngestURL, "/")+ingestRuntimePath, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.DeviceKey))
	req.Header.Set("Content-Type", contentTypeJSONL)
	req.Header.Set("Content-Encoding", contentEncoding)
	req.Header.Set("User-Agent", asymptoteUserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("managed ingest rejected the device key (HTTP 401): revoked, expired, or its approver left the organization")
	default:
		return fmt.Errorf("managed ingest upload failed: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
}
