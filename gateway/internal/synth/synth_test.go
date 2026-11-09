package synth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func checkJSON(t *testing.T, what string, body []byte) {
	t.Helper()
	if !json.Valid(body) {
		t.Fatalf("%s is not valid JSON: %.200s", what, body)
	}
}

func TestDeterministic(t *testing.T) {
	a, b := NewScript(3, 2, 20, 500), NewScript(3, 2, 20, 500)
	c := NewScript(3, 3, 20, 500)
	user := []byte("u")
	for k := 1; k <= 20; k++ {
		ra, rb := AppendJoin(nil, a.AnthropicRequest(nil, k, user, Benign)), AppendJoin(nil, b.AnthropicRequest(nil, k, user, Benign))
		if !bytes.Equal(ra, rb) || !bytes.Equal(a.SSE(k, Benign, true), b.SSE(k, Benign, true)) || !bytes.Equal(a.ChatResponse(k, Benign), b.ChatResponse(k, Benign)) {
			t.Fatalf("turn %d differs between equal scripts", k)
		}
	}
	if bytes.Equal(AppendJoin(nil, a.AnthropicRequest(nil, 20, user, Benign)), AppendJoin(nil, c.AnthropicRequest(nil, 20, user, Benign))) {
		t.Fatal("variants render the same session")
	}
}

// TestShapes checks the LiteLLM pieces form JSON in the envelope LiteLLM sends, and that a long
// SSE reply is large enough to split into thousands of chunks.
func TestShapes(t *testing.T) {
	s := NewScript(1, 0, 5, 1000)
	for k := 1; k <= 5; k++ {
		for _, bad := range []Bad{Benign, Injection} {
			env := [][]byte{[]byte(`{"structured_messages":[`)}
			env = s.ChatMessages(env, k, bad)
			env = append(env, []byte(`],"tools":`), s.ChatTools(), []byte(`,"texts":[`))
			env = s.Texts(env, k, bad)
			env = append(env, []byte(`]}`))
			checkJSON(t, fmt.Sprintf("litellm request envelope turn %d", k), AppendJoin(nil, env))
		}
		for _, bad := range []Bad{Benign, CurlPipe} {
			checkJSON(t, "tool_calls", s.ChatToolCalls(k, bad))
			checkJSON(t, "text", s.ChatText(k, bad))
		}
	}
	if n := len(s.SSE(1, Benign, true)); n < 64*1000 {
		t.Fatalf("long SSE is %d bytes", n)
	}
}
