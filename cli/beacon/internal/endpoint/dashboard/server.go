package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/tokens"
)

const DefaultAddr = "127.0.0.1:8765"

//go:embed static/*
var staticFiles embed.FS

type Options struct {
	Addr     string
	LogPath  string
	UserMode bool
}

type StatusResponse struct {
	Version     string                     `json:"version"`
	ConfigPath  string                     `json:"config_path"`
	LogPath     string                     `json:"log_path"`
	RuntimeLog  lifecycle.RuntimeLogSource `json:"runtime_log"`
	Harnesses   interface{}                `json:"harnesses"`
	Collector   interface{}                `json:"collector"`
	Service     interface{}                `json:"service"`
	Diagnostics interface{}                `json:"diagnostics"`
}

func Handler(opts Options) (http.Handler, error) {
	runtimeLog := lifecycle.ResolveRuntimeLog(opts.UserMode, opts.LogPath)
	opts.LogPath = runtimeLog.EffectiveLogPath
	opts.UserMode = runtimeLog.EffectiveUserMode
	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		status := lifecycle.GetStatus(opts.UserMode, opts.LogPath)
		writeJSON(w, StatusResponse{
			Version:     status.Version,
			ConfigPath:  status.ConfigPath,
			LogPath:     status.LogPath,
			RuntimeLog:  runtimeLog,
			Harnesses:   status.Harnesses,
			Collector:   status.Collector,
			Service:     status.Service,
			Diagnostics: status.Diagnostics,
		})
	})
	mux.HandleFunc("/api/summary", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		events, err := ReadEvents(opts.LogPath, parseQuery(r, maxEventLimit))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, BuildSummary(events))
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		events, err := ReadEvents(opts.LogPath, parseQuery(r, defaultEventLimit))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, events)
	})
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		// Token rollups need the full matching log, not the latest page.
		query := parseQuery(r, maxEventLimit)
		query.NoLimit = true
		result, err := ReadEvents(opts.LogPath, query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Feed Aggregate in chronological (append) order so cumulative metric
		// series resolve correctly when a batch of datapoints shares the same
		// second-resolution timestamp.
		SortRecordsAppendOrder(result.Events)
		session := strings.TrimSpace(query.Session)
		events := make([]schema.Event, 0, len(result.Events))
		for _, record := range result.Events {
			// Match the session case-insensitively (consistent with the
			// case-insensitive event query) so totals/grouping stay aligned
			// with the per-step drilldown.
			if session != "" && (record.Event.Session == nil || !strings.EqualFold(record.Event.Session.ID, session)) {
				continue
			}
			events = append(events, record.Event)
		}
		writeJSON(w, tokens.Aggregate(events, tokenOptions(r)))
	})
	mux.HandleFunc("/api/event", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		id := r.URL.Query().Get("id")
		record, ok, err := FindEvent(opts.LogPath, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("event not found"))
			return
		}
		writeJSON(w, record)
	})
	mux.Handle("/", http.FileServer(http.FS(staticRoot)))
	return securityHeaders(mux), nil
}

func ListenAndServe(opts Options) error {
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	if err := ValidateLoopbackAddr(opts.Addr); err != nil {
		return err
	}
	handler, err := Handler(opts)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              opts.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func ValidateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dashboard address must bind to a loopback IP")
	}
	return nil
}

func URL(addr string) string {
	if addr == "" {
		addr = DefaultAddr
	}
	return "http://" + addr
}

func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func parseQuery(r *http.Request, fallbackLimit int) EventQuery {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit == 0 {
		limit = fallbackLimit
	}
	query := EventQuery{
		Limit:      limit,
		Q:          q.Get("q"),
		Harness:    q.Get("harness"),
		Model:      q.Get("model"),
		Action:     q.Get("action"),
		Severity:   q.Get("severity"),
		Category:   q.Get("category"),
		Repository: q.Get("repository"),
		Session:    q.Get("session"),
		Trace:      q.Get("trace"),
		File:       q.Get("file"),
		Command:    q.Get("command"),
		MCP:        q.Get("mcp"),
		Approval:   q.Get("approval"),
		Decision:   q.Get("decision"),
		Policy:     q.Get("policy"),
		Review:     q.Get("review"),
		WazuhLevel: q.Get("wazuh_level"),
	}
	if since := q.Get("since"); since != "" {
		if parsed, err := time.Parse(time.RFC3339, since); err == nil {
			query.Since = parsed
		}
	}
	if until := q.Get("until"); until != "" {
		if parsed, err := time.Parse(time.RFC3339, until); err == nil {
			query.Until = parsed
		}
	}
	return query
}

func tokenOptions(r *http.Request) tokens.Options {
	q := r.URL.Query()
	opts := tokens.Options{SessionID: q.Get("session")}
	if bucket := q.Get("bucket"); bucket != "" {
		if parsed, err := time.ParseDuration(bucket); err == nil && parsed > 0 {
			opts.BucketSize = parsed
		}
	}
	if top, err := strconv.Atoi(q.Get("top")); err == nil && top > 0 {
		opts.TopLimit = top
	}
	return opts
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
