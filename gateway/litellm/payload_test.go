package litellm

import (
	"bytes"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
	"github.com/asymptote-labs/agent-beacon/gateway/llm/codec"
)

// decodeRequest must decode every valid payload as decoding it whole does: names in any case,
// escaped names, duplicates (the last wins), unknown members and nulls.
func TestDecodeRequestMatchesFullDecode(t *testing.T) {
	for _, body := range []string{
		`{"input_type":"request","litellm_call_id":"c1","litellm_trace_id":"t1","model":"gpt-4o","structured_messages":[{"role":"user","content":"hi"}],"texts":["hi"],"tools":[{"type":"function","function":{"name":"f"}}],"tool_calls":null,"request_headers":{"user-agent":"claude-cli/2"},"request_data":{"user_api_key_user_id":"u","user_api_key_team_id":"t"},"images":[],"litellm_logging_obj":{"a":[1,{"b":"]"}]}}`,
		`{"Input_Type":"response","MODEL":"m","Tool_Calls":[{"id":"x","function":{"name":"Bash","arguments":"{}"}}],"Texts":["a","b"],"REQUEST_DATA":{"user_api_key_alias":"k"}}`,
		`{"model":"a","model":"b","structured_messages":[1],"structured_messages":[2],"request_data":{"user_api_key_org_id":"o"},"request_data":{"user_api_key_hash":"h"}}`,
		`{"structured\u005fmessages":[{"role":"system","content":"s"}],"mod\u0065l":"esc"}`,
		`{"structured_messages":null,"texts":null,"tools":null,"request_headers":null}`,
		`{}`,
	} {
		var got, want request
		if err := decodeRequest([]byte(body), &got); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if err := json.Unmarshal([]byte(body), &want, decodeOptions); err != nil {
			t.Fatalf("%s: full decode: %v", body, err)
		}
		got.messages = nil // the walk's split of the messages, which no decode makes
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", body, got, want)
		}
	}
}

// A request body built over its payload must decode exactly as the one built in a buffer of its
// own, whatever order, spelling and spacing the payload gives its members, and with the walk's
// split of its messages as without; a payload without room after the messages for tools placed
// before them is copied instead.
func TestRequestBodyInPlaceDecodesAsCopied(t *testing.T) {
	msgs := `[{"role":"system","content":"SYS"},{"role":"user","content":"hello"},{"role":"assistant","content":null,"tool_calls":[{"id":"tu1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},{"role":"tool","content":"a\nb","tool_call_id":"tu1"}]`
	tools := `[{"type":"function","function":{"name":"Bash","description":"run","parameters":{"type":"object"}}}]`
	long := `"texts":["` + strings.Repeat("x", 200) + `"]`
	for _, tc := range []struct {
		payload string
		inPlace bool
	}{
		{`{"input_type":"request","model":"gpt-4o","structured_messages":` + msgs + `,"texts":["SYS","hello"],"tools":` + tools + `}`, true},
		{`{"tools":` + tools + `,"structured_messages":` + msgs + `,"model":"gpt-4o","input_type":"request"}`, false},
		{`{"tools":` + tools + `,"structured_messages":` + msgs + `,"model":"gpt-4o",` + long + `}`, true},
		{`{"structured_messages":` + msgs + `,"tools":` + tools + `}`, true},
		{`{"structured_messages":` + msgs + `}`, true},
		{`{"Tools" : ` + tools + ` , "Structured_Messages" :` + msgs + `,"MODEL":"m"}`, false},
		{`{"structured\u005fmessages":` + msgs + `,"tool\u0073":` + tools + `,"model":"caf\u00e9 \"q\""}`, true},
		{`{"structured_messages":[1],"structured_messages":` + msgs + `,"tools":null,"model":"m"}`, true},
		{`{"structured_messages" :` + "\n\t " + msgs + " ,\n" + `"tools":` + tools + "\n}", true},
	} {
		var req request
		if err := decodeRequest([]byte(tc.payload), &req); err != nil {
			t.Fatalf("%s: %v", tc.payload, err)
		}
		copied, _, err := requestBody(req, nil)
		if err != nil {
			t.Fatal(err)
		}
		buf := []byte(tc.payload)
		if err := decodeRequest(buf, &req); err != nil {
			t.Fatal(err)
		}
		built, split, err := requestBody(req, buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, in := sliceOf(buf, built); in != tc.inPlace {
			t.Errorf("%s: built in place %v, want %v", tc.payload, in, tc.inPlace)
		}
		if tc.inPlace && len(split) != 4 {
			t.Errorf("%s: %d messages split, want 4", tc.payload, len(split))
		}
		want, err := codec.DecodeRequest(llm.OpenAIChat, copied)
		if err != nil {
			t.Fatalf("%s: %s: %v", tc.payload, copied, err)
		}
		got, err := codec.DecodeRequest(llm.OpenAIChat, built)
		if err != nil {
			t.Fatalf("%s: %s: %v", tc.payload, built, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %s\n     %+v\nwant %s\n     %+v", tc.payload, built, got, copied, want)
		}
		plain, perr := codec.SkimRequest(llm.OpenAIChat, built)
		splitSkim, serr := codec.SkimSplitRequest(llm.OpenAIChat, built, split)
		if perr != nil || serr != nil || !reflect.DeepEqual(splitSkim, plain) {
			t.Errorf("%s: skimmed with the walk's split %+v (%v), without %+v (%v)", tc.payload, splitSkim, serr, plain, perr)
		}
	}
}

// Tools before the messages with no room after them for the tools leave the payload as it was.
func TestInPlaceRefusesWithoutRoom(t *testing.T) {
	payload := []byte(`{"tools":[1,2,3,4,5,6,7,8,9],"structured_messages":[1]}`)
	orig := bytes.Clone(payload)
	tools, msgs := payload[9:28], payload[51:54]
	if string(tools) != "[1,2,3,4,5,6,7,8,9]" || string(msgs) != "[1]" {
		t.Fatalf("test slices %s %s", tools, msgs)
	}
	if _, ok := inPlace(payload, "m", msgs, tools); ok {
		t.Fatal("built a body the payload has no room for")
	}
	if !bytes.Equal(payload, orig) {
		t.Fatalf("payload modified: %s", payload)
	}
}
