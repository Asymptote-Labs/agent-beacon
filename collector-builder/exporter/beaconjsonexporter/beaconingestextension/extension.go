// Package beaconingestextension is the collector side of the restricted log access mode: it
// listens on the local ingest socket (a named pipe on Windows) and appends the events hooks send
// there to the runtime log, so a system-mode endpoint can keep that log 0640 root:beacon-log, or on
// Windows closed to everyone but SYSTEM, Administrators and the readers group, instead of writable
// by every user.
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
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.uber.org/zap"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/runtimelog"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

type ingest struct {
	cfg    *Config
	logger *zap.Logger
	names  *nameCache

	// listen, serveListener and appendLine are the seams tests replace.
	listen        func(path string) (localingest.Listener, error)
	serveListener func(localingest.Listener) net.Listener
	appendLine    func(path string, line []byte, rotateBytes int64, rotateArchives int) error

	mu     sync.Mutex
	server *localingest.Server
	done   chan error
	// stopRetry and retried are set while Start is still waiting to listen (see retryableListenError).
	stopRetry chan struct{}
	retried   chan struct{}
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

// Start creates the socket or pipe and serves it in the background.
//
// On Unix a socket another running collector still answers on, or a non-socket at the path, fails
// the start: two writers serving one log is a problem to surface, not to work around, and the
// socket's directory is root's, so nobody else can hold the name.
//
// On Windows the pipe's name is in a namespace every account can create in, so an account that
// takes it first must not be able to keep the whole collector down (the beaconjson exporter and the
// OTLP receivers with it). There a listen that failed because the name is taken is reported as a
// recoverable error and retried every listenRetryInterval until it succeeds or the collector stops
// (any other listen error still fails the start); meanwhile hooks refuse the
// impostor's pipe, which is not owned by SYSTEM or Administrators, and spool their events.
func (e *ingest) Start(_ context.Context, host component.Host) error {
	if !asymptoteobserve.RuntimeLogAccessRestricted(e.cfg.LogPath) {
		// Harmless -- the log is simply also writable directly -- but not what install sets up,
		// so it is worth a line in the collector's log.
		e.logger.Warn("beacon_ingest is serving a runtime log that is not in the restricted access mode",
			zap.String("log_path", e.cfg.LogPath))
	}
	l, err := e.listen(e.socketPath())
	if err == nil {
		e.serve(host, l)
		return nil
	}
	retryable := retryableListenError(err)
	err = fmt.Errorf("beacon_ingest: listen on %s: %w", e.socketPath(), err)
	if !retryable {
		return err
	}
	e.logger.Error("beacon_ingest cannot listen yet; hooks spool their events until it can", zap.Error(err))
	if host != nil {
		componentstatus.ReportStatus(host, componentstatus.NewRecoverableErrorEvent(err))
	}
	stop, retried := make(chan struct{}), make(chan struct{})
	e.mu.Lock()
	e.stopRetry, e.retried = stop, retried
	e.mu.Unlock()
	go e.retryListen(host, stop, retried)
	return nil
}

// listenRetryLogEvery is how many failed retries pass between warnings: about a minute at the
// default interval.
const listenRetryLogEvery = 12

// retryListen listens again every listenRetryInterval until it succeeds or stop is closed.
func (e *ingest) retryListen(host component.Host, stop <-chan struct{}, retried chan<- struct{}) {
	defer close(retried)
	ticker := time.NewTicker(listenRetryInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		l, err := e.listen(e.socketPath())
		if err != nil {
			// Once a minute or so, so a long wait stays visible without filling the log.
			if failures++; failures%listenRetryLogEvery == 0 {
				e.logger.Warn("beacon_ingest still cannot listen", zap.Error(err), zap.Int("attempts", failures))
			}
			continue
		}
		e.serve(host, l)
		if host != nil {
			componentstatus.ReportStatus(host, componentstatus.NewEvent(componentstatus.StatusOK))
		}
		return
	}
}

// serve serves l in the background until Shutdown or a serve error.
func (e *ingest) serve(host component.Host, l localingest.Listener) {
	server := &localingest.Server{
		Append:             e.append,
		LogPath:            e.cfg.LogPath,
		LookupName:         e.names.lookup,
		MaxConns:           e.cfg.MaxConnections,
		MaxConnsPerUID:     e.cfg.MaxConnectionsPerUID,
		RateBytesPerSecond: e.cfg.RateBytesPerSecond,
		RateBurstBytes:     e.cfg.RateBurstBytes,
		// One fixed message with the detail as a field, so the collector's log sampler groups
		// these; the server already limits how often it reports any one account.
		Logf: func(format string, args ...interface{}) {
			e.logger.Warn("beacon_ingest refused input", zap.String("detail", fmt.Sprintf(format, args...)))
		},
	}
	done := make(chan error, 1)
	go func() {
		var served net.Listener = l
		if e.serveListener != nil {
			served = e.serveListener(l)
		}
		err := server.Serve(served)
		if err != nil && !errors.Is(err, localingest.ErrServerClosed) {
			// Serving stopped on its own. The server has closed its listener, so the socket is
			// gone and hooks fall back to spooling at once; saying so loudly, and as a fatal
			// status, gets the collector restarted instead of running on without its socket.
			e.logger.Error("beacon_ingest stopped serving", zap.Error(err), zap.String("socket", e.socketPath()))
			if host != nil {
				componentstatus.ReportStatus(host, componentstatus.NewFatalErrorEvent(fmt.Errorf("beacon_ingest: %w", err)))
			}
		}
		done <- err
	}()
	e.mu.Lock()
	e.server, e.done = server, done
	e.mu.Unlock()
	e.logger.Info("beacon_ingest listening", zap.String("socket", e.socketPath()), zap.String("log_path", e.cfg.LogPath))
}

// Shutdown stops serving, waits for connections in progress to finish their writes, and removes
// the socket, so hooks find no socket and spool until the collector is back.
func (e *ingest) Shutdown(context.Context) error {
	e.mu.Lock()
	stop, retried := e.stopRetry, e.retried
	e.stopRetry, e.retried = nil, nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
		<-retried // after this nothing else starts serving
	}
	e.mu.Lock()
	server, done := e.server, e.done
	e.server, e.done = nil, nil
	e.mu.Unlock()
	if server == nil {
		return nil
	}
	err := server.Close()
	<-done // a serve error was reported when it happened
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
