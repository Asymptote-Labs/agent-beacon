package beaconjsonexporter

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/component"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// In restricted log mode (asymptoteobserve.RestrictedLog) hooks cannot write the runtime log, so
// they send each finished line to this exporter over a Unix socket and it appends them through the
// same lock, deduplication and rotation as its own events. The collector is then the log's only
// writer.
//
// One beaconjson component is built once per signal it exports (logs, traces, metrics), and all of
// them share one socket, so the listener is shared by path and closed with the last of them.

type hookSocketShare struct {
	socket *asymptoteobserve.RuntimeLogSocket
	refs   int
}

var (
	hookSocketsMu sync.Mutex
	hookSockets   = map[string]*hookSocketShare{}
)

// listenHookSocket is a variable for tests.
var listenHookSocket = asymptoteobserve.ListenRuntimeLogSocket

func (e *beaconExporter) start(context.Context, component.Host) error {
	if e.cfg.HookSocket == "" {
		return nil
	}
	hookSocketsMu.Lock()
	defer hookSocketsMu.Unlock()
	if share, ok := hookSockets[e.cfg.HookSocket]; ok {
		share.refs++
		return nil
	}
	w := e.writer
	socket, err := listenHookSocket(e.cfg.HookSocket, func(line []byte) error {
		return appendJSONL(w.path, line, w.rotateBytes, w.rotateArchives)
	})
	if err != nil {
		return fmt.Errorf("beaconjson hook socket: %w", err)
	}
	hookSockets[e.cfg.HookSocket] = &hookSocketShare{socket: socket, refs: 1}
	return nil
}

func (e *beaconExporter) shutdown(context.Context) error {
	if e.cfg.HookSocket == "" {
		return nil
	}
	hookSocketsMu.Lock()
	defer hookSocketsMu.Unlock()
	share, ok := hookSockets[e.cfg.HookSocket]
	if !ok {
		return nil
	}
	share.refs--
	if share.refs > 0 {
		return nil
	}
	delete(hookSockets, e.cfg.HookSocket)
	return share.socket.Close()
}
