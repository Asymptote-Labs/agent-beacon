package jsonscan

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
)

// BenchmarkObject walks a guardrail-sized payload: a long session's messages and tools.
func BenchmarkObject(b *testing.B) {
	s := synth.NewScript(1, 0, 300, 3000)
	var pieces [][]byte
	pieces = s.ChatRequest(pieces, 300, nil, synth.Benign)
	body := synth.AppendJoin(nil, pieces)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := Object(body, func(string, []byte) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
