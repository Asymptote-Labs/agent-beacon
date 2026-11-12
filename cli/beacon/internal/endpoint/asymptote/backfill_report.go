package asymptote

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// BackfillReport is what a connect tells Beacon Cloud about the session history it staged, so the
// dashboard can expect it before the forwarder has shipped it: counts only.
type BackfillReport struct {
	SessionCount int `json:"session_count"`
	EventCount   int `json:"event_count"`
}

const (
	// backfillReportBudget bounds the whole report, both attempts included.
	backfillReportBudget = 3 * time.Second
	// backfillReportRetryDelay is the pause before the one retry.
	backfillReportRetryDelay = 250 * time.Millisecond
)

// Swapped by tests.
var (
	sendBackfillReport                = ReportBackfill
	backfillReportClient *http.Client = nil
)

// ReportBackfill POSTs report to <ingestURL>/v1/ingest/backfill, authenticated with the device key
// exactly like the forwarder's runtime uploads. It is best-effort: an older server answers 404, a
// busy one 503, and none of it matters to the connect, so every error is dropped. A 5xx or a
// transport error is retried once.
func ReportBackfill(ctx context.Context, userMode bool, ingestURL string, report BackfillReport) {
	if !IsSecureURL(ingestURL) {
		return
	}
	key, err := ReadDeviceKey(userMode)
	if err != nil {
		return
	}
	body, err := json.Marshal(report)
	if err != nil {
		return
	}
	client := backfillReportClient
	if client == nil {
		client = &http.Client{Timeout: backfillReportBudget}
	}
	ctx, cancel := context.WithTimeout(ctx, backfillReportBudget)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backfillReportRetryDelay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ingestURL+BackfillReportPath, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "beacon-cli")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return
		}
	}
}
