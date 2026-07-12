package cloudshuttle

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
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
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	defaultLogPath     = "/tmp/beacon/runtime.jsonl"
	defaultStatePath   = "/tmp/beacon/shuttle-state.json"
	defaultTokenURI    = "https://oauth2.googleapis.com/token"
	defaultGCSEndpoint = "https://storage.googleapis.com"
	defaultS3Region    = "us-east-1"
	contentTypeJSONL   = "application/x-ndjson"
	contentEncoding    = "gzip"
	uploadGCS          = "gcs"
	uploadS3           = "s3"
)

var httpClient = http.DefaultClient
var nowUTC = func() time.Time { return time.Now().UTC() }

type Config struct {
	Upload         string
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
	S3Region       string
	S3AccessKeyID  string
	S3SecretKey    string
	S3SessionToken string
	S3Endpoint     string
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
	Provider   string `json:"provider,omitempty"`
	RunID      string `json:"run_id,omitempty"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func ConfigFromEnv() Config {
	statePath := firstEnvDefault(defaultStatePath, "BEACON_CLOUD_SHUTTLE_STATE")
	upload := cloudUploadFromEnv()
	bucket := strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_BUCKET"))
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_PREFIX")), "/")
	if upload == uploadS3 {
		bucket = strings.TrimSpace(os.Getenv("BEACON_CLOUD_S3_BUCKET"))
		prefix = strings.Trim(strings.TrimSpace(os.Getenv("BEACON_CLOUD_S3_PREFIX")), "/")
	}
	return Config{
		Upload:         upload,
		LogPath:        firstEnvDefault(defaultLogPath, "BEACON_CLOUD_LOG_PATH", "BEACON_ENDPOINT_LOG", "BEACON_LOG_PATH", "BEACON_RUNTIME_LOG"),
		StatePath:      statePath,
		Bucket:         bucket,
		Prefix:         prefix,
		CredentialsB64: strings.TrimSpace(os.Getenv("BEACON_CLOUD_GCS_CREDENTIALS_B64")),
		Provider:       firstEnvDefault("claude_code_web", "BEACON_RUN_PROVIDER"),
		RunID:          resolveRunID(),
		UserID:         firstEnvDefault("unknown", "BEACON_CLOUD_USER_ID_HASH", "BEACON_CLOUD_USER_ID"),
		Repository:     firstEnv("BEACON_RUN_REPOSITORY"),
		GCSEndpoint:    firstEnvDefault(defaultGCSEndpoint, "BEACON_CLOUD_GCS_ENDPOINT"),
		S3Region:       firstEnvDefault(defaultS3Region, "BEACON_CLOUD_S3_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"),
		S3AccessKeyID:  firstEnv("AWS_ACCESS_KEY_ID"),
		S3SecretKey:    firstEnv("AWS_SECRET_ACCESS_KEY"),
		S3SessionToken: firstEnv("AWS_SESSION_TOKEN"),
		S3Endpoint:     firstEnv("BEACON_CLOUD_S3_ENDPOINT"),
	}
}

func MaybeUpload(ctx context.Context, force bool) error {
	return Upload(ctx, ConfigFromEnv(), force)
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
	cfg = normalizeConfig(cfg)
	if !uploadConfigured(cfg) {
		return nil
	}
	if strings.TrimSpace(cfg.RunID) == "" {
		return nil
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
		return nil
	}
	snapshot, cleanup, err := snapshotLog(cfg.LogPath)
	if err != nil {
		return err
	}
	defer cleanup()
	objectName := uploadObjectName(cfg)
	if err := uploadSnapshot(ctx, cfg, objectName, snapshot); err != nil {
		return err
	}
	return writeState(cfg.StatePath, state{
		LastUpload: time.Now().UTC().Format(time.RFC3339),
		LastSize:   info.Size(),
		LastObject: objectName,
		Provider:   cfg.Provider,
		RunID:      cfg.RunID,
	})
}

func uploadObjectName(cfg Config) string {
	st, err := readState(cfg.StatePath)
	if err == nil && stateObjectMatchesConfig(st, cfg) {
		return strings.TrimSpace(st.LastObject)
	}
	return ObjectName(cfg)
}

func stateObjectMatchesConfig(st state, cfg Config) bool {
	objectName := strings.TrimSpace(st.LastObject)
	if objectName == "" {
		return false
	}
	if st.Provider != "" || st.RunID != "" {
		return st.Provider == cfg.Provider && st.RunID == cfg.RunID
	}
	provider := cleanKeyPart(defaultString(cfg.Provider, "unknown"))
	runID := cleanKeyPart(defaultString(cfg.RunID, "unknown"))
	return strings.HasSuffix(path.Base(objectName), "-"+provider+"-"+runID+".jsonl.gz")
}

func ObjectName(cfg Config) string {
	now := nowUTC()
	parts := cleanKeyParts(cfg.Prefix)
	parts = append(parts, "runtime", "date="+now.Format("2006-01-02"))
	filename := strings.Join([]string{
		fmt.Sprintf("%d", now.Unix()),
		cleanKeyPart(defaultString(cfg.Provider, "unknown")),
		cleanKeyPart(defaultString(cfg.RunID, "unknown")),
	}, "-") + ".jsonl.gz"
	parts = append(parts, filename)
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

func uploadSnapshot(ctx context.Context, cfg Config, objectName, filePath string) error {
	compressedPath, cleanup, err := gzipSnapshot(filePath)
	if err != nil {
		return err
	}
	defer cleanup()
	switch cfg.Upload {
	case uploadS3:
		return uploadS3Object(ctx, cfg, objectName, compressedPath)
	default:
		token, err := accessToken(ctx, cfg.CredentialsB64)
		if err != nil {
			return err
		}
		return uploadGCSObject(ctx, cfg.GCSEndpoint, cfg.Bucket, objectName, compressedPath, token)
	}
}

func gzipSnapshot(filePath string) (string, func(), error) {
	source, err := os.Open(filePath)
	if err != nil {
		return "", func() {}, err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(filePath), "beacon-cloud-*.jsonl.gz")
	if err != nil {
		return "", func() {}, err
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	gz := gzip.NewWriter(temp)
	if _, err := io.Copy(gz, source); err != nil {
		_ = gz.Close()
		_ = temp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := gz.Close(); err != nil {
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

func uploadGCSObject(ctx context.Context, endpoint, bucket, objectName, filePath, token string) error {
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
	req.Header.Set("Content-Encoding", contentEncoding)
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

func uploadS3Object(ctx context.Context, cfg Config, objectName, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	payloadHash, err := hashOpenFile(file)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	endpoint := strings.TrimRight(cfg.S3Endpoint, "/")
	if endpoint == "" {
		endpoint = "https://s3." + cfg.S3Region + ".amazonaws.com"
	}
	uploadURL := endpoint + "/" + escapePath(cfg.Bucket) + "/" + escapeObjectName(objectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, file)
	if err != nil {
		return err
	}
	if info, err := file.Stat(); err == nil {
		req.ContentLength = info.Size()
	}
	req.Header.Set("Content-Type", contentTypeJSONL)
	req.Header.Set("Content-Encoding", contentEncoding)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	if cfg.S3SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", cfg.S3SessionToken)
	}
	signS3Request(req, cfg, payloadHash)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("S3 upload failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func signS3Request(req *http.Request, cfg Config, payloadHash string) {
	amzDate := req.Header.Get("X-Amz-Date")
	dateStamp := amzDate[:8]
	credentialScope := dateStamp + "/" + cfg.S3Region + "/s3/aws4_request"
	headers := map[string]string{
		"content-encoding":     req.Header.Get("Content-Encoding"),
		"content-type":         req.Header.Get("Content-Type"),
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if token := req.Header.Get("X-Amz-Security-Token"); token != "" {
		headers["x-amz-security-token"] = token
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, key := range keys {
		canonicalHeaders.WriteString(key)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headers[key]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(keys, ";")
	canonicalRequest := strings.Join([]string{
		req.Method,
		awsCanonicalURI(req.URL.Path),
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(s3SigningKey(cfg.S3SecretKey, dateStamp, cfg.S3Region), stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cfg.S3AccessKeyID+"/"+credentialScope+", SignedHeaders="+signedHeaders+", Signature="+signature)
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
	cfg = normalizeConfig(cfg)
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
	if st, err := readState(cfg.StatePath); err == nil && st.LastObject != "" && uploadConfigured(cfg) {
		snapshot, cleanup, err := snapshotLog(cfg.LogPath)
		if err == nil {
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if uploadErr := uploadSnapshot(ctx, cfg, st.LastObject, snapshot); uploadErr == nil {
				return os.Remove(cfg.LogPath)
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
	return firstEnv("BEACON_RUN_ID", "CLAUDE_CODE_REMOTE_SESSION_ID")
}

func cloudUploadFromEnv() string {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("BEACON_CLOUD_UPLOAD")))
	switch value {
	case uploadS3:
		return uploadS3
	case uploadGCS:
		return uploadGCS
	}
	return uploadGCS
}

func normalizeConfig(cfg Config) Config {
	cfg.Upload = strings.ToLower(strings.TrimSpace(cfg.Upload))
	if cfg.Upload == "" {
		if cfg.S3AccessKeyID != "" || cfg.S3SecretKey != "" || cfg.S3Region != "" || strings.Contains(cfg.S3Endpoint, "s3") {
			cfg.Upload = uploadS3
		} else {
			cfg.Upload = uploadGCS
		}
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
	if cfg.S3Region == "" {
		cfg.S3Region = defaultS3Region
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	return cfg
}

func uploadConfigured(cfg Config) bool {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return false
	}
	switch cfg.Upload {
	case uploadS3:
		return strings.TrimSpace(cfg.S3AccessKeyID) != "" && strings.TrimSpace(cfg.S3SecretKey) != ""
	default:
		return strings.TrimSpace(cfg.CredentialsB64) != ""
	}
}

func hashOpenFile(file *os.File) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func s3SigningKey(secret, dateStamp, region string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, "s3")
	return hmacSHA256(serviceKey, "aws4_request")
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
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

func awsCanonicalURI(pathValue string) string {
	if pathValue == "" {
		return "/"
	}
	var out strings.Builder
	for i := 0; i < len(pathValue); i++ {
		b := pathValue[i]
		switch {
		case b == '/':
			out.WriteByte('/')
		case (b >= 'A' && b <= 'Z') ||
			(b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~':
			out.WriteByte(b)
		default:
			out.WriteByte('%')
			out.WriteByte("0123456789ABCDEF"[b>>4])
			out.WriteByte("0123456789ABCDEF"[b&0x0f])
		}
	}
	return out.String()
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
