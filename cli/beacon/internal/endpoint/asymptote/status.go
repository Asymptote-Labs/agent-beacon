package asymptote

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

var randRead = rand.Read

// CredentialCheckTimeout bounds the network call status makes; status must stay fast offline.
const CredentialCheckTimeout = 3 * time.Second

// ManagedIngestStatus is what `beacon endpoint status` reports about managed forwarding.
type ManagedIngestStatus struct {
	Enabled          bool           `json:"enabled"`
	IngestURL        string         `json:"ingest_url,omitempty"`
	DeviceID         string         `json:"device_id,omitempty"`
	KeyPrefix        string         `json:"key_prefix,omitempty"`
	OrganizationID   string         `json:"organization_id,omitempty"`
	OrganizationName string         `json:"organization_name,omitempty"`
	Email            string         `json:"email,omitempty"`
	EnrolledAt       string         `json:"enrolled_at,omitempty"`
	VectorBin        string         `json:"vector_bin,omitempty"`
	Forwarder        service.Status `json:"forwarder"`
	// Credential is "valid", "revoked", "unknown" (network unavailable) or "" when not
	// enabled. Revoked covers every 401 cause: revoked or expired key, or the approving
	// user leaving the organization.
	Credential        string `json:"credential,omitempty"`
	CredentialMessage string `json:"credential_message,omitempty"`
	BufferBytes       int64  `json:"buffer_bytes"`
	Message           string `json:"message,omitempty"`
}

// StatusOptions tunes Status; zero values are the production defaults.
type StatusOptions struct {
	HTTPClient *http.Client
	// SkipCredentialCheck avoids the network call (tests, offline diagnostics).
	SkipCredentialCheck bool
}

// Status describes the managed-ingest state of this endpoint. It never reads the device key
// into the caller's structures: the key is used for one HEAD-equivalent GET and discarded.
func Status(userMode bool, opts StatusOptions) ManagedIngestStatus {
	enrollment, err := LoadEnrollment(userMode)
	if err != nil {
		status := ManagedIngestStatus{Enabled: false}
		if !errors.Is(err, ErrNotEnrolled) {
			status.Message = err.Error()
		}
		return status
	}
	status := ManagedIngestStatus{
		Enabled:          true,
		IngestURL:        enrollment.IngestURL,
		DeviceID:         enrollment.DeviceID,
		KeyPrefix:        enrollment.KeyPrefix,
		OrganizationID:   enrollment.OrganizationID,
		OrganizationName: enrollment.OrganizationName,
		Email:            enrollment.Email,
		EnrolledAt:       enrollment.EnrolledAt.UTC().Format(time.RFC3339),
		VectorBin:        enrollment.VectorBin,
		Forwarder:        ForwarderStatus(userMode),
		BufferBytes:      BufferBytes(userMode),
	}
	if opts.SkipCredentialCheck {
		return status
	}
	status.Credential, status.CredentialMessage = CheckCredential(userMode, enrollment.IngestURL, opts.HTTPClient)
	return status
}

// CheckCredential asks the ingest service whether the stored key is still accepted.
func CheckCredential(userMode bool, ingestURL string, client *http.Client) (state string, message string) {
	key, err := ReadDeviceKey(userMode)
	if err != nil {
		return "unknown", err.Error()
	}
	if client == nil {
		client = &http.Client{Timeout: CredentialCheckTimeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), CredentialCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ingestURL+CredentialCheckPath, nil)
	if err != nil {
		return "unknown", err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "beacon-cli")
	resp, err := client.Do(req)
	if err != nil {
		return "unknown", fmt.Sprintf("ingest service unreachable: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return "valid", ""
	case http.StatusUnauthorized:
		return "revoked", "the device key is revoked, expired, or its approver is no longer a member; run `beacon endpoint connect` to re-enroll"
	case http.StatusForbidden:
		return "revoked", "managed ingest is not enabled for this organization"
	default:
		return "unknown", fmt.Sprintf("ingest service answered HTTP %d", resp.StatusCode)
	}
}
