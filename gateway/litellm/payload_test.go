package litellm

import (
	json "encoding/json/v2"
	"reflect"
	"testing"
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
