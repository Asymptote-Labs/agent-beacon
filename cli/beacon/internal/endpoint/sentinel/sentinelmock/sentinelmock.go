// Package sentinelmock stands in for Microsoft Entra ID and the Azure Monitor Logs Ingestion API,
// so the Sentinel forwarder can be tested end to end without an Azure tenant.
//
// One HTTPS server answers both: POST /{tenant}/oauth2/v2.0/token issues a bearer token for the
// configured app registration, and POST /dataCollectionRules/{id}/streams/{stream} accepts rows the
// way the real API does (a JSON array, at most 1 MB, with that token). Point Vector's endpoint at
// URL and AZURE_AUTHORITY_HOST at URL, and trust the CA written to CAFile.
package sentinelmock

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"
)

// MaxBodyBytes is the Logs Ingestion API's request limit.
const MaxBodyBytes = 1 << 20

// Options configures the app registration and DCR the mock accepts, and the faults it injects.
type Options struct {
	TenantID       string
	ClientID       string
	ClientSecret   string // empty accepts a certificate assertion instead
	DCRImmutableID string
	Stream         string
	// CAFile receives the PEM CA certificate to trust. Required.
	CAFile string
	// RowsFile, when set, receives every accepted row as one JSON line.
	RowsFile string
	// Addr is the listen address; empty is 127.0.0.1 on a free port.
	Addr string
	// FailTokens answers the first N token requests with 500.
	FailTokens int
	// IngestStatuses answers the first non-empty ingest requests with these statuses, in order.
	IngestStatuses []int
}

// Row is one record the API accepted.
type Row struct {
	TimeGenerated string `json:"TimeGenerated"`
	RawData       string `json:"RawData"`
}

// Server is a running mock.
type Server struct {
	URL string

	opts     Options
	srv      *httptest.Server
	mu       sync.Mutex
	token    string
	rows     []Row
	tokens   int
	ingests  int
	rejected int
	rowsOut  *os.File
}

// Start runs a mock until Close.
func Start(opts Options) (*Server, error) {
	if opts.CAFile == "" {
		return nil, fmt.Errorf("sentinelmock: CAFile is required")
	}
	tlsConfig, err := selfSigned(opts.CAFile)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, token: randomHex(24)}
	if opts.RowsFile != "" {
		if s.rowsOut, err = os.OpenFile(opts.RowsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return nil, err
		}
	}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	if opts.Addr != "" {
		listener, err := net.Listen("tcp", opts.Addr)
		if err != nil {
			return nil, err
		}
		s.srv.Listener.Close()
		s.srv.Listener = listener
	}
	s.srv.TLS = tlsConfig
	s.srv.StartTLS()
	s.URL = s.srv.URL
	return s, nil
}

// Close stops the server.
func (s *Server) Close() {
	s.srv.Close()
	if s.rowsOut != nil {
		s.rowsOut.Close()
	}
}

// Rows returns every row accepted so far.
func (s *Server) Rows() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Row(nil), s.rows...)
}

// Counts reports token requests, non-empty ingest requests, and requests refused by injected faults.
func (s *Server) Counts() (tokens, ingests, rejected int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens, s.ingests, s.rejected
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
		s.serveToken(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/dataCollectionRules/"):
		s.serveIngest(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.tokens++
	fail := s.tokens <= s.opts.FailTokens
	s.mu.Unlock()
	if fail {
		http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusInternalServerError)
		return
	}
	tenant := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/oauth2/v2.0/token")
	if err := r.ParseForm(); err != nil || tenant != s.opts.TenantID ||
		r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_id") != s.opts.ClientID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if s.opts.ClientSecret != "" {
		if r.PostForm.Get("client_secret") != s.opts.ClientSecret {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
	} else if r.PostForm.Get("client_assertion") == "" ||
		r.PostForm.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token_type": "Bearer", "expires_in": 3599, "ext_expires_in": 3599, "access_token": s.token,
	})
}

func (s *Server) serveIngest(w http.ResponseWriter, r *http.Request) {
	want := "/dataCollectionRules/" + s.opts.DCRImmutableID + "/streams/" + s.opts.Stream
	if r.URL.Path != want || r.URL.Query().Get("api-version") == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such DCR or stream: " + r.URL.Path})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "not JSON"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body) > MaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "over 1 MB"})
		return
	}
	var rows []Row
	if err := json.Unmarshal(body, &rows); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a JSON array of rows: " + err.Error()})
		return
	}
	if len(rows) == 0 {
		w.WriteHeader(http.StatusNoContent) // the sink's healthcheck
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ingests++
	if s.ingests <= len(s.opts.IngestStatuses) {
		s.rejected++
		writeJSON(w, s.opts.IngestStatuses[s.ingests-1], map[string]string{"error": "injected"})
		return
	}
	for _, row := range rows {
		if row.TimeGenerated == "" || row.RawData == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a row without TimeGenerated or RawData"})
			return
		}
	}
	s.rows = append(s.rows, rows...)
	if s.rowsOut != nil {
		for _, row := range rows {
			line, _ := json.Marshal(row)
			s.rowsOut.Write(append(line, '\n'))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// selfSigned makes a CA, writes it to caFile, and returns a server config with a leaf for
// 127.0.0.1 and localhost signed by it.
func selfSigned(caFile string) (*tls.Config, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	notBefore := time.Now().Add(-time.Hour)
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Beacon Sentinel mock CA " + randomHex(4)},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    notBefore,
		NotAfter:     notBefore.Add(48 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}},
	}, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
