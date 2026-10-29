package asymptoteobserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Restricted log mode is an opt-in shape for a system-mode endpoint in which ordinary users can
// neither read nor write the machine-wide runtime log.
//
// In the default shape the log is 0666 because hooks run as each logged-in user and append to it
// themselves. In restricted mode the hooks hand each line to the root collector over a local Unix
// socket instead, the collector is the only process that writes the log, and the log family is
// 0640 root:beacon-log in a 0750 root:beacon-log directory. A log shipper (Sentinel, Splunk UF,
// Filebeat) reads it by being added to the beacon-log group.
//
// The mode is declared by a root-owned marker file the install writes into the system config
// directory. All three writers consult it through RuntimeLogModes and OpenRuntimeLogFile, so they
// cannot disagree about the log's mode or group, and the hook adapter consults it to decide
// between appending and sending. It is Unix-only.
const (
	// RestrictedRuntimeLogFileMode is the mode of every file in the log family in restricted mode.
	RestrictedRuntimeLogFileMode os.FileMode = 0o640
	// RestrictedRuntimeLogDirMode keeps the log directory closed to everyone outside the group.
	RestrictedRuntimeLogDirMode os.FileMode = 0o750
	// RuntimeLogGroup is the group that may read a restricted log.
	RuntimeLogGroup = "beacon-log"
	// RestrictedLogAccess is the value of config.json's log_access for restricted mode.
	RestrictedLogAccess = "restricted"
	// DefaultRuntimeLogSocketPath is where the root collector listens for hook lines.
	DefaultRuntimeLogSocketPath = "/var/run/beacon-agent/hooks.sock"
	// RuntimeLogSocketTimeout bounds one hook's send, connect to reply.
	RuntimeLogSocketTimeout = 3 * time.Second
	// maxRuntimeLogLineBytes is the largest line the socket accepts, matching every writer's
	// event-size limit (MaxEventBytes in the CLI, 64 KiB in the hook adapter and the exporter).
	maxRuntimeLogLineBytes = 64 * 1024
	// restrictedLogMarkerName is the marker's file name inside the system config directory.
	restrictedLogMarkerName = "log-access.json"
)

// RestrictedLogMarkerPath is the marker that declares restricted mode, in the system config
// directory (the CLI's endpointconfig.SystemBaseDir, which a test keeps equal). Empty off
// Linux and macOS, where restricted mode does not exist. A variable for tests.
var RestrictedLogMarkerPath = defaultRestrictedLogMarkerPath()

func defaultRestrictedLogMarkerPath() string {
	switch runtime.GOOS {
	case "linux":
		return filepath.Join("/etc/beacon/endpoint", restrictedLogMarkerName)
	case "darwin":
		return filepath.Join("/Library/Application Support/Beacon/Endpoint", restrictedLogMarkerName)
	}
	return ""
}

// RestrictedLog is the marker's content.
type RestrictedLog struct {
	// LogAccess is always RestrictedLogAccess; a marker saying anything else is ignored.
	LogAccess string `json:"log_access"`
	// LogPath is the system runtime log. Restricted mode covers every file in its directory.
	LogPath string `json:"log_path"`
	// Socket is where the root collector accepts hook lines.
	Socket string `json:"socket"`
	// Group and GID name the reader group. The GID is recorded because Go cannot resolve a macOS
	// directory-service group without cgo, and every writer needs it to chown what it creates.
	Group string `json:"group"`
	GID   int    `json:"gid"`
}

// trustRestrictedLogMarker decides whether a marker file may be believed: a regular file owned by
// root and writable only by root. A variable so tests outside root can stand one in.
var trustRestrictedLogMarker = markerOwnedByRoot

// LoadRestrictedLog returns the restricted-mode declaration, and false when this machine is not
// in restricted mode -- no marker, an untrusted or malformed one, or a platform without the mode.
func LoadRestrictedLog() (RestrictedLog, bool) {
	path := RestrictedLogMarkerPath
	if path == "" {
		return RestrictedLog{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !trustRestrictedLogMarker(info) {
		return RestrictedLog{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RestrictedLog{}, false
	}
	var marker RestrictedLog
	if err := json.Unmarshal(data, &marker); err != nil {
		return RestrictedLog{}, false
	}
	if marker.LogAccess != RestrictedLogAccess || !filepath.IsAbs(marker.LogPath) || !filepath.IsAbs(marker.Socket) || marker.GID <= 0 {
		return RestrictedLog{}, false
	}
	return marker, true
}

// Covers reports whether path is in the restricted log's directory: the log, its lock, its
// archives and the other files Beacon writes beside it.
func (r RestrictedLog) Covers(path string) bool {
	if r.LogPath == "" || strings.TrimSpace(path) == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return pathWithin(abs, filepath.Dir(r.LogPath))
}

// RestrictedLogFor returns the restricted-mode declaration when it covers path.
func RestrictedLogFor(path string) (RestrictedLog, bool) {
	marker, ok := LoadRestrictedLog()
	if !ok || !marker.Covers(path) {
		return RestrictedLog{}, false
	}
	return marker, true
}

// WriteRestrictedLogMarker writes the marker atomically, root-owned 0644 so every hook can read
// it. Only the CLI's install calls it.
func WriteRestrictedLogMarker(marker RestrictedLog) error {
	path := RestrictedLogMarkerPath
	if path == "" {
		return fmt.Errorf("restricted log mode is not supported on %s", runtime.GOOS)
	}
	marker.LogAccess = RestrictedLogAccess
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+restrictedLogMarkerName+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RemoveRestrictedLogMarker returns the machine to the shared log shape. Missing is not an error.
func RemoveRestrictedLogMarker() error {
	path := RestrictedLogMarkerPath
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ValidateRuntimeLogLine checks a line a hook sent before the collector appends it: one JSON object
// of at most 64 KiB carrying the endpoint event envelope. It does not judge the content -- a hook
// runs as the user and can say what it likes -- only that the line is an event, so the socket
// cannot be used to put arbitrary bytes or a broken line into the log.
func ValidateRuntimeLogLine(line []byte) error {
	line = bytes.TrimSuffix(line, []byte("\n"))
	if len(line) == 0 {
		return errors.New("empty line")
	}
	if len(line) > maxRuntimeLogLineBytes {
		return fmt.Errorf("line exceeds %d bytes", maxRuntimeLogLineBytes)
	}
	if bytes.ContainsAny(line, "\r\n") {
		return errors.New("line contains a line break")
	}
	var envelope struct {
		Vendor        string `json:"vendor"`
		Product       string `json:"product"`
		SchemaVersion string `json:"schema_version"`
		Event         *struct {
			Action string `json:"action"`
		} `json:"event"`
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("not a JSON event: %w", err)
	}
	if decoder.More() {
		return errors.New("more than one JSON value")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data after the event")
	}
	if envelope.Vendor == "" || envelope.Product == "" || envelope.SchemaVersion == "" || envelope.Event == nil || envelope.Event.Action == "" {
		return errors.New("missing vendor, product, schema_version or event.action")
	}
	return nil
}

// SendRuntimeLogLine hands one line to the collector listening on socket and waits for it to be
// written. It is how hooks write a restricted log.
func SendRuntimeLogLine(socket string, line []byte, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = RuntimeLogSocketTimeout
	}
	if !bytes.HasSuffix(line, []byte("\n")) {
		line = append(append([]byte{}, line...), '\n')
	}
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return fmt.Errorf("the Beacon collector is not accepting events on %s: %w", socket, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(line); err != nil {
		return fmt.Errorf("send the event to %s: %w", socket, err)
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, 1024)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("no reply from %s: %w", socket, err)
	}
	reply = strings.TrimSpace(reply)
	if reply == "ok" {
		return nil
	}
	return fmt.Errorf("the Beacon collector refused the event: %s", strings.TrimPrefix(reply, "error: "))
}

// RuntimeLogSocket is the collector's end of the hook socket.
type RuntimeLogSocket struct {
	path     string
	listener net.Listener
	appendFn func([]byte) error
	slots    chan struct{}
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

// maxRuntimeLogSocketConns bounds the connections handled at once, so a local user opening many
// connections cannot exhaust the collector.
const maxRuntimeLogSocketConns = 64

// ListenRuntimeLogSocket starts accepting hook lines on path and passes each valid one to
// appendFn, which writes it to the log. The socket's directory is created 0755 and the socket
// itself is 0666: every local user's hooks must be able to connect, and connecting is all they can
// do. A stale socket left by a collector that did not shut down is replaced; anything else at the
// path is refused rather than removed.
func ListenRuntimeLogSocket(path string, appendFn func([]byte) error) (*RuntimeLogSocket, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("hook socket path %q must be absolute", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the hook socket directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode().Type() != os.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove the stale hook socket: %w", err)
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		// Close removes the file; do it ourselves so a restart is not blocked by a stale one.
		unixListener.SetUnlinkOnClose(true)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("open the hook socket to local users: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &RuntimeLogSocket{
		path:     path,
		listener: listener,
		appendFn: appendFn,
		slots:    make(chan struct{}, maxRuntimeLogSocketConns),
		cancel:   cancel,
	}
	s.wg.Add(1)
	go s.serve(ctx)
	return s, nil
}

// Path is the socket's path.
func (s *RuntimeLogSocket) Path() string { return s.path }

// Close stops accepting, waits for lines in flight and removes the socket.
func (s *RuntimeLogSocket) Close() error {
	s.cancel()
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

func (s *RuntimeLogSocket) serve(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(conn, "error: busy\n")
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			s.handle(conn)
		}()
	}
}

func (s *RuntimeLogSocket) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(RuntimeLogSocketTimeout))
	line, err := readRuntimeLogLine(conn)
	if err == nil {
		err = ValidateRuntimeLogLine(line)
	}
	if err == nil {
		err = s.appendFn(line)
	}
	if err != nil {
		_, _ = io.WriteString(conn, "error: "+singleLine(err.Error())+"\n")
		return
	}
	_, _ = io.WriteString(conn, "ok\n")
}

// readRuntimeLogLine reads one newline-terminated line of at most the event limit.
func readRuntimeLogLine(r io.Reader) ([]byte, error) {
	reader := bufio.NewReaderSize(io.LimitReader(r, maxRuntimeLogLineBytes+2), 4096)
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxRuntimeLogLineBytes+1 {
			return nil, fmt.Errorf("line exceeds %d bytes", maxRuntimeLogLineBytes)
		}
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, errors.New("line has no terminating newline")
		}
		return nil, err
	}
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}

// UseRestrictedLogMarkerForTest points the marker at path and trusts it whoever owns it, for tests
// in the modules that share this package (the hook adapter, the exporter, the CLI), which cannot
// create a root-owned file. It returns the function that restores both.
func UseRestrictedLogMarkerForTest(path string) (restore func()) {
	previousPath, previousTrust := RestrictedLogMarkerPath, trustRestrictedLogMarker
	RestrictedLogMarkerPath = path
	trustRestrictedLogMarker = func(os.FileInfo) bool { return true }
	return func() {
		RestrictedLogMarkerPath, trustRestrictedLogMarker = previousPath, previousTrust
	}
}
