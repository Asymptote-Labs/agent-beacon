package asymptote

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BackfillReport is what a connect tells Beacon Cloud about the session history it staged, so the
// dashboard can expect it before the forwarder has shipped it: counts only.
type BackfillReport struct {
	SessionCount int `json:"session_count"`
	EventCount   int `json:"event_count"`
}

const (
	// backfillReportAttempts bounds the requests one report makes.
	backfillReportAttempts = 5
	// backfillReportBackoff is the first pause between attempts; each later one doubles it.
	backfillReportBackoff = 500 * time.Millisecond
	// backfillReportMaxRetryAfter caps how long a Retry-After header can make one attempt wait.
	backfillReportMaxRetryAfter = 5 * time.Second
)

// Swapped by tests.
var (
	sendBackfillReport                = ReportBackfill
	backfillReportClient *http.Client = nil
	// backfillReportBudget bounds the whole report, every attempt and pause included. The report
	// is sent just as the forwarder starts uploading the backfill, when the server's per-device
	// ingest limit is most likely to answer 429, so it waits that out for a while.
	backfillReportBudget = 15 * time.Second
	// backfillReportSleep waits d, or returns false when ctx ends first.
	backfillReportSleep = func(ctx context.Context, d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
)

// ReportBackfill POSTs report to <ingestURL>/v1/ingest/backfill, authenticated with the device key
// exactly like the forwarder's runtime uploads. It is best-effort: an older server answers 404, and
// none of it matters to the connect, so every error is dropped. A transport error, a 5xx or a 429
// is retried, up to backfillReportAttempts requests within backfillReportBudget, after a backoff
// or the server's Retry-After; any other 4xx is final.
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
	deadline, _ := ctx.Deadline()
	backoff := backfillReportBackoff
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ingestURL+BackfillReportPath, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "beacon-cli")
		wait := backoff
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if !backfillReportRetryable(resp.StatusCode) {
				return
			}
			if after, ok := backfillReportRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
				wait = after
			}
		}
		if attempt == backfillReportAttempts || ctx.Err() != nil {
			return
		}
		// A wait the budget cannot cover would only hold the connect until it runs out.
		if time.Until(deadline) <= wait || !backfillReportSleep(ctx, wait) {
			return
		}
		backoff *= 2
	}
}

// backfillReportRetryable reports whether a report answered with status is worth sending again:
// 429 is the server's per-device ingest limit, busy while the forwarder uploads the backfill, and a
// 5xx is a server that may be back in a moment.
func backfillReportRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// backfillReportRetryAfter reads a Retry-After header, in seconds or as an HTTP date, capped at
// backfillReportMaxRetryAfter. ok is false when there is none or it cannot be read.
func backfillReportRetryAfter(header string, now time.Time) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	var wait time.Duration
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		wait = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(header); err == nil {
		wait = at.Sub(now)
	} else {
		return 0, false
	}
	return min(max(wait, 0), backfillReportMaxRetryAfter), true
}
