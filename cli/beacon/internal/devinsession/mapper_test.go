package devinsession

import (
	"fmt"
	"strings"
	"testing"
)

// Event ids are the dedupe contract: keyed on message_id so Devin's re-inserted rows produce the
// same event id, and namespaced so they cannot collide with another collector's.
func TestDevinEventIDIsDeterministicAndUUIDShaped(t *testing.T) {
	coordinate := "quiet-meadow:msg-1:agent.message"
	first, second := devinEventID(coordinate), devinEventID(coordinate)
	if first != second {
		t.Fatalf("devinEventID is not deterministic: %q != %q", first, second)
	}
	if len(first) != 36 || first[8] != '-' || first[13] != '-' || first[18] != '-' || first[23] != '-' {
		t.Fatalf("devinEventID = %q, not UUID shaped", first)
	}
	if first[14] != '5' {
		t.Fatalf("devinEventID = %q, want a v5-shaped id", first)
	}
	if devinEventID("other:msg-1:agent.message") == first {
		t.Fatal("different coordinates produced the same id")
	}
}

func TestMessageIDDigestMatchesTheRingFormat(t *testing.T) {
	digest := messageIDDigest("msg-1")
	if len(digest) != 16 {
		t.Fatalf("digest = %q, want a 16-hex-char prefix", digest)
	}
	if digest != messageIDDigest("msg-1") || digest == messageIDDigest("msg-2") {
		t.Fatal("digests are not stable and distinct")
	}
}

func TestRecentRingCapsAtTheLimit(t *testing.T) {
	m := &mapper{cursor: &Cursor{}, recent: map[string]bool{}}
	for i := 0; i < recentMessageIDLimit+10; i++ {
		m.recordMessageID(fmt.Sprintf("msg-%d", i))
	}
	if len(m.cursor.Recent) != recentMessageIDLimit || len(m.recent) != recentMessageIDLimit {
		t.Fatalf("ring = %d/%d entries, want capped at %d", len(m.cursor.Recent), len(m.recent), recentMessageIDLimit)
	}
	// The oldest entries fell out of both the slice and the membership view.
	if m.seenMessageID("msg-0") {
		t.Fatal("the oldest message id is still in the ring")
	}
	if !m.seenMessageID(fmt.Sprintf("msg-%d", recentMessageIDLimit+9)) {
		t.Fatal("the newest message id is not in the ring")
	}
}

func TestMCPForCall(t *testing.T) {
	server, tool := mcpForCall(toolCall{Name: "mcp__github__create_issue"})
	if server != "github" || tool != "create_issue" {
		t.Fatalf("native mcp name = %q/%q", server, tool)
	}
	server, tool = mcpForCall(toolCall{Name: "mcp_call_tool", Arguments: map[string]interface{}{"server_name": "linear", "tool_name": "get_issue"}})
	if server != "linear" || tool != "get_issue" {
		t.Fatalf("mcp_call_tool = %q/%q", server, tool)
	}
	if server, tool := mcpForCall(toolCall{Name: "exec"}); server != "" || tool != "" {
		t.Fatalf("a plain tool resolved as MCP: %q/%q", server, tool)
	}
}

func TestFailureReasonShapes(t *testing.T) {
	if f := failureReason("ToolError"); !f.present || f.label != "ToolError" || f.rejected {
		t.Fatalf("string failure_reason = %#v", f)
	}
	if f := failureReason(map[string]interface{}{"Rejected": map[string]interface{}{"interactive": true}}); !f.rejected || !f.interactive {
		t.Fatalf("rejected failure_reason = %#v", f)
	}
	if f := failureReason(nil); f.present {
		t.Fatalf("absent failure_reason = %#v", f)
	}
}

func TestDefaultStatePathIsUnderBeaconStateDir(t *testing.T) {
	path := DefaultStatePath()
	if !strings.HasSuffix(path, "devin.json") || !strings.Contains(path, "state") {
		t.Fatalf("DefaultStatePath = %q", path)
	}
}
