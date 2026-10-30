package localingest

import (
	"fmt"
	"sync"
	"testing"
)

// The helpers below serve the exchange tests on every platform: the Unix socket tests and the
// Windows named pipe tests drive the same Server and Client.

type recorder struct {
	mu    sync.Mutex
	lines [][]byte
	fail  func(n int, line []byte) error
}

func (r *recorder) append(line []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(len(r.lines), line); err != nil {
			return err
		}
	}
	r.lines = append(r.lines, append([]byte(nil), line...))
	return nil
}

func (r *recorder) setFail(fail func(n int, line []byte) error) {
	r.mu.Lock()
	r.fail = fail
	r.mu.Unlock()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}

func lines(t *testing.T, n int) [][]byte {
	t.Helper()
	out := make([][]byte, n)
	for i := range out {
		i := i
		out[i] = validLine(t, func(e map[string]interface{}) {
			e["event"].(map[string]interface{})["id"] = fmt.Sprintf("id-%d", i)
			e["message"] = fmt.Sprintf("m-%d", i)
			e["user"] = map[string]interface{}{"name": "root", "uid": "0"}
		})
	}
	return out
}

// opening is the preamble and a header naming no log, which a server without a LogPath accepts.
const opening = Preamble + "\n" + `{"log":""}` + "\n"
