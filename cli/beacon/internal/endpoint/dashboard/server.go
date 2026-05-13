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
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
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
	Version          string                          `json:"version"`
	ConfigPath       string                          `json:"config_path"`
	LogPath          string                          `json:"log_path"`
	ContentRetention endpointconfig.ContentRetention `json:"content_retention"`
	Harnesses        interface{}                     `json:"harnesses"`
	Collector        interface{}                     `json:"collector"`
	Service          interface{}                     `json:"service"`
	Diagnostics      interface{}                     `json:"diagnostics"`
}

func Handler(opts Options) (http.Handler, error) {
	if opts.LogPath == "" {
		opts.LogPath = writer.DefaultPath(opts.UserMode)
	}
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
		cfg, err := endpointconfig.Load(opts.UserMode)
		if err != nil {
			cfg = endpointconfig.Default(opts.UserMode, opts.LogPath)
		}
		if opts.LogPath != "" {
			cfg.LogPath = opts.LogPath
		}
		writeJSON(w, StatusResponse{
			Version:          status.Version,
			ConfigPath:       status.ConfigPath,
			LogPath:          status.LogPath,
			ContentRetention: cfg.ContentRetention,
			Harnesses:        status.Harnesses,
			Collector:        status.Collector,
			Service:          status.Service,
			Diagnostics:      status.Diagnostics,
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
	return mux, nil
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
		Harness:    q.Get("harness"),
		Action:     q.Get("action"),
		Repository: q.Get("repository"),
		Session:    q.Get("session"),
		File:       q.Get("file"),
		Command:    q.Get("command"),
	}
	if since := q.Get("since"); since != "" {
		if parsed, err := time.Parse(time.RFC3339, since); err == nil {
			query.Since = parsed
		}
	}
	return query
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
