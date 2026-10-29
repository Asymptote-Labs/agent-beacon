// Package beaconingestextension is the collector side of the restricted log access mode: it
// listens on the local ingest socket and appends the events hooks send there to the runtime log,
// so a system-mode endpoint can keep that log 0640 root:beacon-log instead of world-writable.
//
// Each event is stamped with the sender's kernel-verified account (localingest.StampLine) and
// written through the same lock, rotation and file-mode handling as the beaconjson exporter
// (internal/runtimelog), so the two writers stay interchangeable on one log.
package beaconingestextension

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/runtimelog"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

type ingest struct {
	cfg    *Config
	logger *zap.Logger
	names  *nameCache

	// listen and appendLine are the seams tests replace.
	listen     func(path string) (*net.UnixListener, error)
	appendLine func(path string, line []byte, rotateBytes int64, rotateArchives int) error

	mu     sync.Mutex
	server *localingest.Server
	done   chan error
}

func newIngest(cfg *Config, logger *zap.Logger) *ingest {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ingest{
		cfg:        cfg,
		logger:     logger,
		names:      newNameCache(),
		listen:     localingest.Listen,
		appendLine: runtimelog.Append,
	}
}

// Start creates the socket and serves it in the background. A socket another running collector
// still answers on, or a non-socket at the path, fails the start: two writers serving one log, or
// a path someone put something else at, is a configuration problem to surface, not to work around.
func (e *ingest) Start(context.Context, component.Host) error {
	if !asymptoteobserve.RuntimeLogAccessRestricted(e.cfg.LogPath) {
		// Harmless -- the log is simply also writable directly -- but not what install sets up,
		// so it is worth a line in the collector's log.
		e.logger.Warn("beacon_ingest is serving a runtime log that is not in the restricted access mode",
			zap.String("log_path", e.cfg.LogPath))
	}
	l, err := e.listen(e.socketPath())
	if err != nil {
		return fmt.Errorf("beacon_ingest: listen on %s: %w", e.socketPath(), err)
	}
	server := &localingest.Server{
		Append:             e.append,
		LogPath:            e.cfg.LogPath,
		LookupName:         e.names.lookup,
		MaxConns:           e.cfg.MaxConnections,
		MaxConnsPerUID:     e.cfg.MaxConnectionsPerUID,
		RateBytesPerSecond: e.cfg.RateBytesPerSecond,
		RateBurstBytes:     e.cfg.RateBurstBytes,
		Logf: func(format string, args ...interface{}) {
			e.logger.Warn(fmt.Sprintf(format, args...))
		},
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	e.mu.Lock()
	e.server, e.done = server, done
	e.mu.Unlock()
	e.logger.Info("beacon_ingest listening", zap.String("socket", e.socketPath()), zap.String("log_path", e.cfg.LogPath))
	return nil
}

// Shutdown stops serving, waits for connections in progress to finish their writes, and removes
// the socket, so hooks find no socket and spool until the collector is back.
func (e *ingest) Shutdown(context.Context) error {
	e.mu.Lock()
	server, done := e.server, e.done
	e.server, e.done = nil, nil
	e.mu.Unlock()
	if server == nil {
		return nil
	}
	err := server.Close()
	if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, localingest.ErrServerClosed) && err == nil {
		err = serveErr
	}
	return err
}

func (e *ingest) socketPath() string {
	if e.cfg.SocketPath == "" {
		return localingest.DefaultSocketPath
	}
	return e.cfg.SocketPath
}

func (e *ingest) append(line []byte) error {
	rotateBytes := e.cfg.RotateBytes
	if rotateBytes <= 0 {
		rotateBytes = defaultRotateBytes
	}
	archives := e.cfg.RotateArchives
	if archives <= 0 {
		archives = defaultRotateArchives
	}
	out := make([]byte, 0, len(line)+1)
	out = append(append(out, line...), '\n')
	return e.appendLine(e.cfg.LogPath, out, rotateBytes, archives)
}
