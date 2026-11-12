package litellm

import (
	"bytes"
	json "encoding/json/v2"
	"reflect"
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
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", body, got, want)
		}
	}
}

// A request body built over its payload must decode exactly as the one built in a buffer of its
// own, whatever order, spelling and spacing the payload gives its members.
func TestRequestBodyInPlaceDecodesAsCopied(t *testing.T) {
	msgs := `[{"role":"system","content":"SYS"},{"role":"user","content":"hello"},{"role":"assistant","content":null,"tool_calls":[{"id":"tu1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},{"role":"tool","content":"a\nb","tool_call_id":"tu1"}]`
	tools := `[{"type":"function","function":{"name":"Bash","description":"run","parameters":{"type":"object"}}}]`
	for _, payload := range []string{
		`{"input_type":"request","model":"gpt-4o","structured_messages":` + msgs + `,"texts":["SYS","hello"],"tools":` + tools + `}`,
		`{"tools":` + tools + `,"structured_messages":` + msgs + `,"model":"gpt-4o","input_type":"request"}`,
		`{"structured_messages":` + msgs + `,"tools":` + tools + `}`,
		`{"structured_messages":` + msgs + `}`,
		`{"Tools" : ` + tools + ` , "Structured_Messages" :` + msgs + `,"MODEL":"m"}`,
		`{"structured\u005fmessages":` + msgs + `,"tool\u0073":` + tools + `,"model":"caf\u00e9 \"q\""}`,
		`{"structured_messages":[1],"structured_messages":` + msgs + `,"tools":null,"model":"m"}`,
	} {
		var req request
		if err := decodeRequest([]byte(payload), &req); err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		copied, err := requestBody(req, nil)
		if err != nil {
			t.Fatal(err)
		}
		buf := []byte(payload)
		if err := decodeRequest(buf, &req); err != nil {
			t.Fatal(err)
		}
		built, err := requestBody(req, buf)
		if err != nil {
			t.Fatal(err)
		}
		if &built[0] != &buf[0] {
			t.Errorf("%s: body was not built in place", payload)
		}
		want, err := codec.DecodeRequest(llm.OpenAIChat, copied)
		if err != nil {
			t.Fatalf("%s: %s: %v", payload, copied, err)
		}
		got, err := codec.DecodeRequest(llm.OpenAIChat, built)
		if err != nil {
			t.Fatalf("%s: %s: %v", payload, built, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %s\n     %+v\nwant %s\n     %+v", payload, built, got, copied, want)
		}
	}
}

// A member that starts too early to be moved left leaves the payload as it was.
func TestInPlaceRefusesWithoutRoom(t *testing.T) {
	payload := []byte(`{"x":[1],"tools":[2]}`)
	orig := bytes.Clone(payload)
	msgs := payload[5:8] // "messages": needs 11 bytes before it
	if _, ok := inPlace(payload, "m", msgs, payload[17:20]); ok {
		t.Fatal("built a body whose messages start too early")
	}
	if !bytes.Equal(payload, orig) {
		t.Fatalf("payload modified: %s", payload)
	}
}
