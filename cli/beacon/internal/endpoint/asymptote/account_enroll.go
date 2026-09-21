package asymptote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const AccountEnrollPath = "/api/cli/enroll/account"

type AccountEnrollOptions struct {
	BaseURL     string
	AccessToken string
	Device      DeviceInfo
	HTTPClient  *http.Client
}

type accountEnrollRequest struct {
	Device DeviceInfo `json:"device"`
}

// EnrollAccount uses a signed-in CLI identity to mint a device-specific ingest
// credential. The account token authorizes enrollment but is never returned,
// persisted in endpoint state, or exposed to Vector.
func EnrollAccount(ctx context.Context, opts AccountEnrollOptions) (*EnrollResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if !IsSecureURL(baseURL) {
		return nil, fmt.Errorf("account enrollment URL must use https://: %s", baseURL)
	}
	if !strings.HasPrefix(opts.AccessToken, "bcn_cli_") {
		return nil, errors.New("account enrollment needs a Beacon CLI access token")
	}
	if opts.Device.InstallID == "" {
		return nil, errors.New("account enrollment needs an install id")
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &clientCopy
	payload, err := json.Marshal(accountEnrollRequest{Device: opts.Device})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+AccountEnrollPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+opts.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beacon-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("account device enrollment failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var failure struct {
			Detail string `json:"detail"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		detail := strings.TrimSpace(failure.Detail)
		if detail == "" {
			detail = strings.TrimSpace(failure.Error)
		}
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("account device enrollment rejected: %s", detail)
	}
	var result EnrollResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse account enrollment response: %w", err)
	}
	if err := validateEnrollResult(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
