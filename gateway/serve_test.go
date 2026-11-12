package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Admission reserves what ReadBody then holds: the buffer a body is read into, which pooling rounds
// up, not just the body's length; otherwise a full budget holds up to twice its bytes.
func TestBodySizeIsTheBufferRead(t *testing.T) {
	const limit = 64 << 20
	for _, n := range []int{0, 1, 4096, 4097, 300_000, 1 << 24, 1<<24 + 1} {
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, n)))
		reserved := BodySize(r, limit)
		body, free, err := ReadBody(r, limit)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if len(body) != n || int64(cap(body)) != reserved {
			t.Errorf("%d bytes: reserved %d, read %d into a buffer of %d", n, reserved, len(body), cap(body))
		}
		free()
	}
}
