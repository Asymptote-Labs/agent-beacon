package llm

import "testing"

func TestTailStart(t *testing.T) {
	roles := func(rs ...Role) []Message {
		out := make([]Message, len(rs))
		for i, r := range rs {
			out[i] = Message{Role: r}
		}
		return out
	}
	for _, tc := range []struct {
		msgs []Message
		want int
	}{
		{roles(RoleSystem, RoleUser), 0}, // first call: everything is new
		{roles(RoleSystem, RoleUser, RoleAssistant, RoleTool, RoleUser), 3},
		{roles(RoleSystem, RoleUser, RoleAssistant), 3},                        // nothing new yet
		{roles(RoleUser, RoleAssistant, RoleTool, RoleAssistant, RoleTool), 4}, // the last assistant turn
	} {
		if got := TailStart(tc.msgs); got != tc.want {
			t.Errorf("TailStart(%v) = %d, want %d", tc.msgs, got, tc.want)
		}
	}
}

// A part's kind decides its channel before the role of the message carrying it: Anthropic sends
// tool results in user messages, and they are results all the same.
func TestPartChannel(t *testing.T) {
	result := Part{Kind: PartToolResult, Result: &ToolResult{CallID: "c"}}
	for _, tc := range []struct {
		part Part
		role Role
		want Channel
	}{
		{result, RoleUser, ChannelResult},
		{result, RoleTool, ChannelResult},
		{Part{Kind: PartMedia, Media: &Media{Type: "image"}}, RoleUser, ChannelMedia},
		{Part{Kind: PartText, Text: "x"}, RoleSystem, ChannelSystem},
		{Part{Kind: PartText, Text: "x"}, RoleUser, ChannelUser},
		{Part{Kind: PartToolCall, Call: &ToolCall{Name: "Bash"}}, RoleAssistant, ChannelAssistant},
	} {
		if got := tc.part.Channel(tc.role); got != tc.want {
			t.Errorf("%v in %s message: channel %s, want %s", tc.part.Kind, tc.role, got, tc.want)
		}
	}
}

func TestPlainTextLen(t *testing.T) {
	media := Part{Kind: PartMedia, Media: &Media{Type: "image"}}
	text := func(s string) Part { return Part{Kind: PartText, Text: s} }
	for _, p := range []Part{
		text(""), text("héllo"), {Kind: PartReasoning, Text: "why"}, {Kind: PartReasoning}, media,
		{Kind: PartToolCall, Call: &ToolCall{}},
		{Kind: PartToolCall, Call: &ToolCall{ID: "c", Name: "Bash", Arguments: `{"command":"ls"}`}},
		{Kind: PartToolResult, Result: &ToolResult{}},
		{Kind: PartToolResult, Result: &ToolResult{Parts: []Part{text("a")}}},
		{Kind: PartToolResult, Result: &ToolResult{Parts: []Part{text(""), text("a"), media, text(""), {Kind: PartReasoning, Text: "r"}, text("bc"), text("")}}},
		{Kind: PartToolResult, Result: &ToolResult{Parts: []Part{media, text("")}}},
	} {
		if got, want := p.PlainTextLen(), len(p.PlainText()); got != want {
			t.Errorf("PlainTextLen(%+v) = %d, want %d", p, got, want)
		}
	}
}

func TestInventory(t *testing.T) {
	media := Part{Kind: PartMedia, Media: &Media{Type: "image"}}
	inv := Inventory{}
	inv.AddMessages([]Message{
		{Role: RoleSystem, Parts: []Part{{Kind: PartText, Text: "sys"}}},
		{Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "hi"}, media}},
		{Role: RoleAssistant, Parts: []Part{{Kind: PartToolCall, Call: &ToolCall{Name: "Read", Arguments: "{}"}}}},
		{Role: RoleUser, Parts: []Part{{Kind: PartToolResult, Result: &ToolResult{Parts: []Part{{Kind: PartText, Text: "a"}, media, {Kind: PartText, Text: "b"}}}}}},
		{Role: RoleUser, Elided: true},
	})
	inv.AddTools([]Tool{{Name: "Read", Description: "Reads.", Parameters: []byte(`{}`)}})
	inv.AddInventory(Inventory{ChannelUser: {Items: 2, Bytes: 10}, ChannelFormat: {Items: 1, Bytes: 4}})
	want := Inventory{
		ChannelSystem:    {1, 3},
		ChannelUser:      {3, 12},
		ChannelMedia:     {2, 0},
		ChannelAssistant: {1, 7},
		ChannelResult:    {1, 3},
		ChannelTools:     {1, 12},
		ChannelFormat:    {1, 4},
	}
	if len(inv) != len(want) {
		t.Fatalf("inventory %v, want %v", inv, want)
	}
	for ch, v := range want {
		if inv[ch] != v {
			t.Errorf("%s = %+v, want %+v", ch, inv[ch], v)
		}
	}
}
