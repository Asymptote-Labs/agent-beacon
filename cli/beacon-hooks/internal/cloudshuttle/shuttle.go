package cloudshuttle

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
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultLogPath     = "/tmp/beacon/runtime.jsonl"
	defaultStatePath   = "/tmp/beacon/shuttle-state.json"
	defaultTokenURI    = "https://oauth2.googleapis.com/token"
	defaultGCSEndpoint = "https://storage.googleapis.com"
	contentTypeJSONL   = "text/plain; charset=utf-8"
)

var httpClient = http.DefaultClient

type Config struct {
	LogPath        string
	StatePath      string
	Bucket         string
	Prefix         string
	CredentialsB64 string
	Provider       string
	RunID          string
	UserID         string
	Repository     string
	GCSEndpoint    string
}

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type state struct {
	LastUpload string `json:"last_upload,omitempty"`
	LastSize   int64  `json:"last_size,omitempty"`
	LastObject string `json:"last_object,omitempty"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func ConfigFromEnv() Config {
	statePath := firstEnvDefault(defaultStatePath, "BEACON_CLOUD_SHUTTLE_STATE")
	logPath := firstEnvDefault(defaultLogPath, "BEACON_CLOUD_LOG_PATH", "BEACON_ENDPOINT_LOG", "BEACON_LOG_PATH", "BEACON_RUNTIME_LOG")
	return Config{
		LogPath:        logPath,
		StatePath:      statePath,
		Bucket:         strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_BUCKET")),
		Prefix:         strings.Trim(strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_PREFIX")), "/"),
		CredentialsB64: strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_CREDENTIALS_B64")),
		Provider:       firstEnvDefault("claude_code_web", "BEACON_RUN_PROVIDER"),
		RunID:          resolveRunIDFromLog(logPath),
		UserID:         firstEnvDefault("unknown", "BEACON_CLOUD_USER_ID_HASH", "BEACON_CLOUD_USER_ID"),
		Repository:     firstEnv("BEACON_RUN_REPOSITORY"),
		GCSEndpoint:    firstEnvDefault(defaultGCSEndpoint, "BEACON_CLOUD_GCS_ENDPOINT"),
	}
}

func MaybeUpload(ctx context.Context, force bool) error {
	return Upload(ctx, ConfigFromEnv(), force)
}

func Watch(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if err := MaybeUpload(ctx, false); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			if err := MaybeUpload(ctx, false); err != nil {
				return err
			}
		}
	}
}

func ResetFromEnv() error {
	cfg := ConfigFromEnv()
	if strings.TrimSpace(os.Getenv("BEACON_ORIGIN")) != "cloud" {
		return nil
	}
	if err := preserveExistingLog(cfg); err != nil {
		return err
	}
	for _, path := range []string{cfg.LogPath + ".lock", cfg.StatePath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func Upload(ctx context.Context, cfg Config, force bool) error {
	if strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.CredentialsB64) == "" {
		return nil
	}
	if strings.TrimSpace(cfg.RunID) == "" {
		return nil
	}
	if cfg.LogPath == "" {
		cfg.LogPath = defaultLogPath
	}
	if cfg.StatePath == "" {
		cfg.StatePath = defaultStatePath
	}
	if cfg.GCSEndpoint == "" {
		cfg.GCSEndpoint = defaultGCSEndpoint
	}
	info, err := os.Stat(cfg.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	if !force {
		if st, err := readState(cfg.StatePath); err == nil && st.LastSize == info.Size() && st.LastObject == ObjectName(cfg) {
			return nil
		}
	}
	snapshot, cleanup, err := snapshotLog(cfg.LogPath)
	if err != nil {
		return err
	}
	defer cleanup()
	token, err := accessToken(ctx, cfg.CredentialsB64)
	if err != nil {
		return err
	}
	objectName := ObjectName(cfg)
	if err := uploadObject(ctx, cfg.GCSEndpoint, cfg.Bucket, objectName, snapshot, token); err != nil {
		return err
	}
	return writeState(cfg.StatePath, state{
		LastUpload: time.Now().UTC().Format(time.RFC3339),
		LastSize:   info.Size(),
		LastObject: objectName,
	})
}

func ObjectName(cfg Config) string {
	parts := cleanKeyParts(cfg.Prefix)
	parts = append(parts, "provider="+cleanKeyPart(defaultString(cfg.Provider, "unknown")))
	if cfg.UserID != "" {
		parts = append(parts, "user_id="+cleanKeyPart(cfg.UserID))
	}
	if cfg.Repository != "" {
		parts = append(parts, cleanKeyParts("repo="+cfg.Repository)...)
	}
	parts = append(parts, "run_id="+cleanKeyPart(defaultString(cfg.RunID, "unknown")))
	parts = append(parts, "runtime.jsonl")
	return path.Join(parts...)
}

func snapshotLog(logPath string) (string, func(), error) {
	lock, err := os.OpenFile(logPath+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return "", func() {}, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
		_ = lock.Close()
		return "", func() {}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	defer func() {
		if err := lock.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "cloudshuttle: failed to close lock file %s.lock: %v\n", logPath, err)
		}
	}()

	source, err := os.Open(logPath)
	if err != nil {
		return "", func() {}, err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return "", func() {}, err
	}
	temp, err := os.CreateTemp(filepath.Dir(logPath), "beacon-cloud-*.jsonl")
	if err != nil {
		return "", func() {}, err
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tempPath, cleanup, nil
}

func accessToken(ctx context.Context, credentialsB64 string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(credentialsB64)
	if err != nil {
		return "", fmt.Errorf("decode GCS credentials: %w", err)
	}
	var account serviceAccount
	if err := json.Unmarshal(decoded, &account); err != nil {
		return "", fmt.Errorf("parse GCS credentials: %w", err)
	}
	if account.ClientEmail == "" || account.PrivateKey == "" {
		return "", errors.New("GCS credentials missing client_email or private_key")
	}
	if account.TokenURI == "" {
		account.TokenURI = defaultTokenURI
	}
	assertion, err := signedJWT(account)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, account.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GCS token exchange failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("parse GCS token response: %w", err)
	}
	if token.AccessToken == "" {
		return "", errors.New("GCS token response missing access_token")
	}
	return token.AccessToken, nil
}

func signedJWT(account serviceAccount) (string, error) {
	now := time.Now().Unix()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":   account.ClientEmail,
		"scope": "https://www.googleapis.com/auth/devstorage.read_write",
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

func uploadObject(ctx context.Context, endpoint, bucket, objectName, filePath, token string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	uploadURL := strings.TrimRight(endpoint, "/") + "/" + escapePath(bucket) + "/" + escapeObjectName(objectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, file)
	if err != nil {
		return err
	}
	if info, err := file.Stat(); err == nil {
		req.ContentLength = info.Size()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentTypeJSONL)
	resp, err := httpClient.Do(req)
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

func readState(path string) (state, error) {
	var st state
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func writeState(path string, st state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func preserveExistingLog(cfg Config) error {
	info, err := os.Stat(cfg.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() == 0 {
		return os.Remove(cfg.LogPath)
	}
	if st, err := readState(cfg.StatePath); err == nil && st.LastObject != "" && cfg.Bucket != "" && cfg.CredentialsB64 != "" {
		snapshot, cleanup, err := snapshotLog(cfg.LogPath)
		if err == nil {
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if token, tokenErr := accessToken(ctx, cfg.CredentialsB64); tokenErr == nil {
				if uploadErr := uploadObject(ctx, cfg.GCSEndpoint, cfg.Bucket, st.LastObject, snapshot, token); uploadErr == nil {
					return os.Remove(cfg.LogPath)
				}
			}
		}
	}
	preservedPath := fmt.Sprintf("%s.previous-%d", cfg.LogPath, time.Now().UTC().UnixNano())
	if err := os.Rename(cfg.LogPath, preservedPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func resolveRunID() string {
	return firstEnv("BEACON_RUN_ID", "CLAUDE_CODE_REMOTE_SESSION_ID", "BEACON_CODEX_SESSION_ID")
}

func resolveRunIDFromLog(logPath string) string {
	if runID := resolveRunID(); runID != "" {
		return runID
	}
	return runIDFromRuntimeLog(logPath)
}

func runIDFromRuntimeLog(logPath string) string {
	if strings.TrimSpace(logPath) == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	var fallback string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if id := nestedString(event, "run", "run_id"); id != "" {
			return id
		}
		if fallback == "" {
			fallback = firstNestedString(event,
				[]string{"session", "id"},
				[]string{"gen_ai", "conversation", "id"},
				[]string{"raw", "attributes", "session_id"},
				[]string{"raw", "attributes", "conversation.id"},
				[]string{"raw", "attributes", "conversation_id"},
				[]string{"raw", "attributes", "gen_ai.conversation.id"},
			)
		}
	}
	return fallback
}

func firstNestedString(values map[string]interface{}, paths ...[]string) string {
	for _, path := range paths {
		if value := nestedString(values, path...); value != "" {
			return value
		}
	}
	return ""
}

func nestedString(values map[string]interface{}, path ...string) string {
	var current interface{} = values
	for _, key := range path {
		next, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current = next[key]
	}
	str, ok := current.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(str)
}

func cleanKeyParts(value string) []string {
	var parts []string
	for _, part := range strings.Split(strings.Trim(value, "/"), "/") {
		if cleaned := cleanKeyPart(part); cleaned != "" {
			parts = append(parts, cleaned)
		}
	}
	return parts
}

func cleanKeyPart(value string) string {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.ReplaceAll(cleaned, "\\", "-")
	cleaned = strings.Trim(cleaned, "/")
	if cleaned == "." || cleaned == ".." {
		return ""
	}
	return cleaned
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

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstEnvDefault(defaultValue string, keys ...string) string {
	if value := firstEnv(keys...); value != "" {
		return value
	}
	return defaultValue
}

func EncodeCredentialsForEnv(data []byte) string {
	return base64.StdEncoding.EncodeToString(bytes.TrimSpace(data))
}
