package portkey

import (
	json "encoding/json/v2"
	"reflect"
	"testing"
)

// decodeEnvelope must decode every valid payload as decoding it whole does: names in any case,
// escaped names, duplicates (the last wins), unknown members, nulls and extra phase fields.
func TestDecodeEnvelopeMatchesFullDecode(t *testing.T) {
	for _, body := range []string{
		`{"request":{"json":{"model":"m","messages":[]},"isStreamingRequest":true,"text":"x"},"response":{"json":null},"requestType":"chatComplete","metadata":{"_user":"alice","n":3},"eventType":"beforeRequestHook","provider":"openai"}`,
		`{"REQUEST":{"JSON":{"a":[1,{"b":"}"}]},"IsStreamingRequest":false},"Response":{"Json":{"choices":[]}},"RequestType":"messages","EventType":"afterRequestHook"}`,
		`{"request":{"json":{"first":1}},"request":{"json":{"second":2}},"eventType":"a","eventType":"b"}`,
		`{"request\u0054ype":"chatComplete","re\u0071uest":{"\u006Ason":[1,2]},"metadata":null}`,
		`{"request":null,"response":{},"metadata":{}}`,
		`{"unknown":{"deep":[[[{"x":"]"}]]]},"eventType":"beforeRequestHook"}`,
		`{}`,
	} {
		var got, want envelope
		if err := decodeEnvelope([]byte(body), &got); err != nil {
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
