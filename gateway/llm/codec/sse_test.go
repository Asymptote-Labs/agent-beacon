package codec

import (
	"slices"
	"testing"
)

type sseEvent struct {
	name string
	data string
}

func collectSSE(body []byte) []sseEvent {
	var out []sseEvent
	for s := newSSEScanner(body); s.next(); {
		out = append(out, sseEvent{string(s.name), string(s.data)})
	}
	return out
}

func TestSSEEvents(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []sseEvent
	}{
		{
			name: "LF with event names",
			body: "event: ping\ndata: {\"type\":\"ping\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			want: []sseEvent{{"ping", `{"type":"ping"}`}, {"message_stop", `{"type":"message_stop"}`}},
		},
		{
			name: "CRLF and multi-line data",
			body: "data: {\"a\":\r\ndata: 1}\r\n\r\ndata: [DONE]\r\n\r\n",
			want: []sseEvent{{"", "{\"a\":\n1}"}, {"", "[DONE]"}},
		},
		{
			name: "CR line endings",
			body: "data: one\r\rdata: two\r\r",
			want: []sseEvent{{"", "one"}, {"", "two"}},
		},
		{
			name: "comments, ids and retry ignored",
			body: ": keep-alive\n\nid: 7\nretry: 1000\ndata: x\n\n: trailing comment\n",
			want: []sseEvent{{"", "x"}},
		},
		{
			name: "final event without blank line",
			body: "data: first\n\ndata: last",
			want: []sseEvent{{"", "first"}, {"", "last"}},
		},
		{
			name: "no space after colon and repeated blank lines",
			body: "\n\nevent:done\ndata:{}\n\n\n\n",
			want: []sseEvent{{"done", "{}"}},
		},
		{
			name: "only one leading space stripped",
			body: "data:  indented\n\n",
			want: []sseEvent{{"", " indented"}},
		},
		{
			name: "empty data line",
			body: "data:\n\n",
			want: []sseEvent{{"", ""}},
		},
		{
			name: "three data lines, mixed endings, an event without data",
			body: "data: a\ndata:\ndata: c\n\ndata: d\ndata: e\r\n\r\nevent: x\n\n",
			want: []sseEvent{{"", "a\n\nc"}, {"", "d\ne"}, {"x", ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectSSE([]byte(tt.body))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("sseEvents = %q, want %q", got, tt.want)
			}
			var ref []sseEvent
			for ev := range refSSEEvents([]byte(tt.body)) {
				ref = append(ref, sseEvent(ev))
			}
			if !slices.Equal(got, ref) {
				t.Fatalf("sseEvents = %q, reference %q", got, ref)
			}
		})
	}
}

func TestIsSSE(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{"event: message_start\ndata: {}\n\n", true},
		{"data: {}\n\n", true},
		{"\r\n  data: {}\n\n", true},
		{": comment\n\ndata: {}\n\n", true},
		{`{"id":"msg_1"}`, false},
		{"  \n{\"data\": 1}", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isSSE([]byte(tt.body)); got != tt.want {
			t.Errorf("isSSE(%q) = %t, want %t", tt.body, got, tt.want)
		}
	}
}
