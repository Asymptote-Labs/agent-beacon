package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ServeHTTP serves h on addr until ctx is cancelled, then shuts down gracefully, returning nil.
// It is the Serve loop of every HTTP callout adapter.
//
// It speaks HTTP/1.1 and unencrypted HTTP/2 (h2c with prior knowledge) on the same port, so a
// gateway that multiplexes callouts over a few connections can. A callout body arrives over a
// fast local link, so a client that takes longer than ReadTimeout to send one is cut off rather
// than allowed to hold a connection open.
func ServeHTTP(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:   h,
		Protocols: protocols,
		// Go's defaults leave a connection 1 MiB of window, so a gateway multiplexing hundreds of
		// large callout bodies over one connection trickles them in: raise both to the maximum.
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams:          1000,
			MaxReceiveBufferPerConnection: 4<<20 - 1,
			MaxReceiveBufferPerStream:     4<<20 - 1,
			MaxReadFrameSize:              1 << 20,
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("listening", "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
}

// DiscardBody reads and drops what is left of a request body (up to limit bytes), for a handler
// that answers without reading it, such as a call refused at admission. On HTTP/1.1, Go's server
// closes a connection whose handler left more than 256 KiB unread, and callout bodies are usually
// larger: refusing calls under load would tear down keep-alive connections as fast as it refused,
// and the reconnect churn exhausts ephemeral ports and socket buffers. On HTTP/2 the unread stream
// is reset without touching the connection, which is cheaper than reading it, so nothing is done.
func DiscardBody(r *http.Request, limit int64) {
	if r.ProtoMajor != 1 {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, limit))
}

// BodySize is what an HTTP adapter reserves at admission for a body it reads up to limit bytes of:
// its declared length, or limit when the length is unknown (chunked) or over the limit.
func BodySize(r *http.Request, limit int64) int64 {
	if r.ContentLength < 0 || r.ContentLength > limit {
		return limit
	}
	return r.ContentLength
}

// ReadBody reads a request body of up to limit bytes into one buffer of its declared length, when
// it declares one: io.ReadAll grows by doubling, which copied a body of a megabyte several times
// over. A body longer than declared, or than limit, is an error.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	if n := r.ContentLength; n >= 0 && n <= limit {
		buf := make([]byte, n)
		if _, err := io.ReadFull(r.Body, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err == nil && int64(len(buf)) > limit {
		err = fmt.Errorf("body exceeds %d bytes", limit)
	}
	return buf, err
}
