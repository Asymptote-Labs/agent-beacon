package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	beaconauth "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

const defaultIngestPath = "/api/v1/ingest/beacon/events"

type Client struct {
	URL        string
	HTTPClient *http.Client
}

func NewClient(ingestURL string, httpClient *http.Client) Client {
	if strings.TrimSpace(ingestURL) == "" {
		ingestURL = beaconauth.ResolveDashboardURL("") + defaultIngestPath
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return Client{
		URL:        strings.TrimRight(ingestURL, "/"),
		HTTPClient: httpClient,
	}
}

func (c Client) UploadBatch(ctx context.Context, token string, reqBody uploadRequest) (uploadResponse, error) {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return uploadResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(data))
	if err != nil {
		return uploadResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "beacon/"+version.GetVersion())

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return uploadResponse{}, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return uploadResponse{}, fmt.Errorf("ingest upload failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if strings.TrimSpace(string(respBody)) == "" {
		return uploadResponse{}, fmt.Errorf("ingest upload response was empty")
	}
	var parsed uploadResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return uploadResponse{}, fmt.Errorf("ingest upload response was invalid: %w", err)
	}
	if parsed.Accepted < 0 || parsed.Rejected < 0 {
		return uploadResponse{}, fmt.Errorf("ingest upload response had negative event counts")
	}
	accounted := parsed.Accepted + parsed.Rejected
	if accounted != len(reqBody.Events) {
		return uploadResponse{}, fmt.Errorf("ingest upload response accounted for %d events, want %d", accounted, len(reqBody.Events))
	}
	return parsed, nil
}
