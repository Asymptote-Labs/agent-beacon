package devincloud

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultGCSEndpoint = "https://storage.googleapis.com"
	gcsScope           = "https://www.googleapis.com/auth/devstorage.read_write"
	uploadContentType  = "text/plain; charset=utf-8"
	// DefaultGCSPrefix matches the prefix used by the in-sandbox hook providers.
	DefaultGCSPrefix = "agent-traces"
)

// GCSUploader PUTs JSONL snapshots to a GCS bucket using a service-account key
// (signed-JWT auth). It is a self-contained port of the beacon-hooks
// cloudshuttle uploader, which lives in an internal package the beacon CLI
// cannot import. Auth uses BEACON_CLOUD_GCS_CREDENTIALS_B64 — the same secret
// the hook providers use — so the binary needs no gcloud and no Google SDK.
type GCSUploader struct {
	bucket     string
	endpoint   string
	account    serviceAccount
	httpClient *http.Client

	token   string
	tokenAt time.Time
}

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// GCSPrefixFromEnv returns the configured object prefix or the default.
func GCSPrefixFromEnv() string {
	if p := strings.Trim(strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_PREFIX")), "/"); p != "" {
		return p
	}
	return DefaultGCSPrefix
}

// NewGCSUploaderFromEnv builds an uploader from BEACON_CLOUD_GCS_* env vars.
// The bool is false (with nil uploader, nil error) when GCS is not configured,
// so callers can treat upload as optional.
func NewGCSUploaderFromEnv() (*GCSUploader, bool, error) {
	bucket := strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_BUCKET"))
	credsB64 := strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_CREDENTIALS_B64"))
	if bucket == "" && credsB64 == "" {
		return nil, false, nil
	}
	if bucket == "" || credsB64 == "" {
		return nil, false, errors.New("both BEACON_CLOUD_GCS_BUCKET and BEACON_CLOUD_GCS_CREDENTIALS_B64 are required for upload")
	}
	credsJSON, err := base64.StdEncoding.DecodeString(credsB64)
	if err != nil {
		return nil, false, fmt.Errorf("decode BEACON_CLOUD_GCS_CREDENTIALS_B64: %w", err)
	}
	var acct serviceAccount
	if err := json.Unmarshal(credsJSON, &acct); err != nil {
		return nil, false, fmt.Errorf("parse GCS credentials JSON: %w", err)
	}
	if acct.ClientEmail == "" || acct.PrivateKey == "" || acct.TokenURI == "" {
		return nil, false, errors.New("GCS credentials missing client_email/private_key/token_uri")
	}
	endpoint := strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultGCSEndpoint
	}
	return &GCSUploader{
		bucket:     bucket,
		endpoint:   endpoint,
		account:    acct,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, true, nil
}

// Upload PUTs data to {endpoint}/{bucket}/{objectName}.
func (u *GCSUploader) Upload(ctx context.Context, objectName string, data []byte) error {
	token, err := u.accessToken(ctx)
	if err != nil {
		return err
	}
	uploadURL := strings.TrimRight(u.endpoint, "/") + "/" + escapePath(u.bucket) + "/" + escapeObjectName(objectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", uploadContentType)
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GCS upload failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (u *GCSUploader) accessToken(ctx context.Context) (string, error) {
	if u.token != "" && time.Since(u.tokenAt) < 45*time.Minute {
		return u.token, nil
	}
	assertion, err := signedJWT(u.account)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.account.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GCS token exchange failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("parse GCS token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("GCS token response missing access_token")
	}
	u.token = tok.AccessToken
	u.tokenAt = time.Now()
	return u.token, nil
}

func signedJWT(account serviceAccount) (string, error) {
	now := time.Now().Unix()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":   account.ClientEmail,
		"scope": gcsScope,
		"aud":   account.TokenURI,
		"iat":   now,
		"exp":   now + 3600,
	}
	encodedHeader, err := encodeJSONSegment(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := encodeJSONSegment(claims)
	if err != nil {
		return "", err
	}
	signingInput := encodedHeader + "." + encodedClaims
	privateKey, err := parseRSAPrivateKey(account.PrivateKey)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeJSONSegment(value interface{}) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func parseRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("GCS private key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("GCS private key is not RSA")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("parse GCS private key")
}

func escapeObjectName(objectName string) string {
	parts := strings.Split(objectName, "/")
	for i, part := range parts {
		parts[i] = escapePath(part)
	}
	return strings.Join(parts, "/")
}

func escapePath(value string) string {
	return (&url.URL{Path: value}).EscapedPath()
}
