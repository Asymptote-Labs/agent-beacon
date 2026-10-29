// Package localingest is the local channel through which processes that are not root hand runtime
// log events to the privileged collector on a system-mode endpoint, instead of appending to the
// shared log themselves.
//
// # Why it exists
//
// A system-mode endpoint has one runtime log, /var/log/beacon-agent/runtime.jsonl, written by the
// root collector and by every logged-in user's hooks. For hooks to append to it, the file has to be
// writable by everyone (0666), and so anyone on the machine can also truncate or rewrite it and read
// every other user's prompts, command output and diffs. The restricted log access mode closes that:
// the collector becomes the only writer, the log becomes 0640 root:beacon-log, and hooks send their
// events here instead.
//
// # The channel
//
// The collector listens on a Unix domain socket (DefaultSocketPath) in a root-owned directory. A
// connection carries one batch:
//
//	client: "BEACON-INGEST/1\n", a Header line, one event per line, then half-closes its write side
//	server: one JSON Response line, after it has handled every line it read
//
// The header names the log the client means to write. A server refuses a batch meant for another
// log, so an event a writer aimed at some other file -- a test's, a custom path -- is written there
// directly rather than landing in the system log.
//
// The server learns who is on the other end from the kernel (SO_PEERCRED on Linux, LOCAL_PEERCRED
// on macOS), never from the event, and StampLine overwrites the event's user with that identity.
// So a user can still describe their own sessions however they like -- their agent runs under their
// control -- but cannot erase, rewrite or read anything already in the log, and cannot write an
// event that claims to be someone else's.
//
// Nothing here touches the network: a Unix socket is a local file, and the client refuses to
// connect to one that is not in a directory only root could have created it in, because a socket a
// user planted there would receive every other user's events.
package localingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	// DefaultSocketPath is where a system-mode collector in the restricted log access mode listens.
	// /var/run is root-owned on Linux (a link to /run) and macOS (a link to /private/var/run), so
	// only root can create the directory the socket lives in.
	DefaultSocketPath = "/var/run/beacon-agent/ingest.sock"

	// Preamble opens every connection. It names the protocol version, so a later version can be
	// told apart, and keeps a stray client that happened to connect from having its bytes taken
	// for events.
	Preamble = "BEACON-INGEST/1"

	// MaxLineBytes is the largest event the server accepts, without its newline. It is the runtime
	// log's own per-event limit, which every writer already enforces before an event gets here.
	MaxLineBytes = 64 * 1024

	// MaxLinesPerConn bounds one batch. A client with more sends another connection.
	MaxLinesPerConn = 256

	// maxResponseBytes bounds what a client reads back.
	maxResponseBytes = 4096
	// maxHeaderBytes bounds the header line.
	maxHeaderBytes = 4096

	// errLogMismatch is the Response.Error a server gives for a batch meant for another log.
	errLogMismatch = "log mismatch"
)

// Header follows the preamble.
type Header struct {
	// Log is the runtime log the client means to write.
	Log string `json:"log"`
}

// ErrLogMismatch is returned by Send when the collector writes a different log from the one the
// client named: the caller writes its own log directly.
var ErrLogMismatch = errors.New("the collector writes a different runtime log")

// ErrRejected marks a line the server will never accept, however often it is sent: malformed,
// too large, or not a Beacon endpoint event. A client drops such lines rather than spooling them.
var ErrRejected = errors.New("event rejected")

// ErrNoSocket is returned by Send when nothing is listening at the socket path because no socket
// exists there: the endpoint is not in the restricted mode, so the caller writes the log as before.
var ErrNoSocket = errors.New("no ingest socket")

// Peer is who the kernel says is on the other end of a connection.
type Peer struct {
	UID uint32
	GID uint32
	// PID is the sending process, or 0 where the platform does not report one.
	PID int32
}

// Response is the server's one reply to a batch.
//
// Lines are handled in order. Processed is how many were handled, each either accepted (written)
// or rejected (dropped for good). A server that hits an error it might not hit next time -- the log
// could not be written, the sender is over its rate -- stops there and reports it in Error, so the
// client keeps lines[Processed:] and tries them again later.
type Response struct {
	Accepted  int    `json:"accepted"`
	Rejected  int    `json:"rejected"`
	Processed int    `json:"processed"`
	Error     string `json:"error,omitempty"`
}

// StampLine validates one line a peer sent and returns the line to append to the runtime log, with
// the event's user replaced by the peer's verified identity. An error wrapping ErrRejected means the
// line is not acceptable at all.
//
// The returned line is re-encoded, so whatever bytes the peer sent, exactly one JSON object on one
// line reaches the log: nothing the sender writes can break the log into extra lines or forge a
// second event inside the first. Numbers are kept exactly as sent. event.id is always derived
// from the stamped line, never taken from the sender.
//
// lookupName resolves a uid to an account name and may be nil; a name it cannot resolve is left
// out rather than taken from the event, which the sender wrote.
func StampLine(line []byte, peer Peer, lookupName func(uid uint32) string) ([]byte, error) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		return nil, fmt.Errorf("%w: empty line", ErrRejected)
	}
	if len(line) > MaxLineBytes {
		return nil, fmt.Errorf("%w: %d bytes is over the %d byte limit", ErrRejected, len(line), MaxLineBytes)
	}
	event, err := decodeObject(line)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if err := checkEnvelope(event); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}

	user := map[string]interface{}{"uid": strconv.FormatUint(uint64(peer.UID), 10)}
	if lookupName != nil {
		if name := strings.TrimSpace(lookupName(peer.UID)); name != "" {
			user["name"] = name
		}
	}
	event["user"] = user

	// The id is derived here, from the line as it will be written, never taken from the sender:
	// otherwise one account could reuse another's event.id, and consumers that deduplicate by id
	// would drop the real event as a repeat. Deterministic, so a resent line keeps its id.
	info := event["event"].(map[string]interface{})
	delete(info, "id")
	unstamped, err := marshalLine(event)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	info["id"] = asymptoteobserve.EventIDForLine(unstamped)
	out, err := marshalLine(event)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if len(out) > MaxLineBytes {
		return nil, fmt.Errorf("%w: %d bytes after stamping is over the %d byte limit", ErrRejected, len(out), MaxLineBytes)
	}
	return out, nil
}

func decodeObject(line []byte) (map[string]interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var event map[string]interface{}
	if err := dec.Decode(&event); err != nil {
		return nil, fmt.Errorf("not a JSON object: %v", err)
	}
	if event == nil {
		return nil, errors.New("not a JSON object")
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value on the line")
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err == nil {
		return nil, errors.New("more than one JSON value on the line")
	}
	return event, nil
}

func checkEnvelope(event map[string]interface{}) error {
	if v, _ := event["vendor"].(string); v != asymptoteobserve.Vendor {
		return fmt.Errorf("vendor must be %q", asymptoteobserve.Vendor)
	}
	if p, _ := event["product"].(string); p != asymptoteobserve.Product {
		return fmt.Errorf("product must be %q", asymptoteobserve.Product)
	}
	if s, _ := event["schema_version"].(string); strings.TrimSpace(s) == "" {
		return errors.New("schema_version is required")
	}
	info, ok := event["event"].(map[string]interface{})
	if !ok {
		return errors.New("event must be an object")
	}
	if action, _ := info["action"].(string); strings.TrimSpace(action) == "" {
		return errors.New("event.action is required")
	}
	return nil
}

func marshalLine(event map[string]interface{}) ([]byte, error) {
	return json.Marshal(event)
}
