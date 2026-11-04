package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The JSON-RPC 2.0 envelope over both transports: ids come back byte for byte, unreadable ids
// come back as null, notifications get nothing, and each failure carries its spec error code.
func TestJSONRPCEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request string
		// wantID is the response's raw id; "" means no response at all.
		wantID   string
		wantCode int
		// wantResult, when set, is the exact raw result.
		wantResult string
	}{
		{name: "id above 2^53", request: `{"jsonrpc":"2.0","id":9007199254740993,"method":"initialize"}`, wantID: `9007199254740993`},
		{name: "max int64 id", request: `{"jsonrpc":"2.0","id":9223372036854775807,"method":"initialize"}`, wantID: `9223372036854775807`},
		{name: "exponent id", request: `{"jsonrpc":"2.0","id":1e3,"method":"initialize"}`, wantID: `1e3`},
		{name: "fractional id", request: `{"jsonrpc":"2.0","id":1.50,"method":"initialize"}`, wantID: `1.50`},
		{name: "negative id", request: `{"jsonrpc":"2.0","id":-1,"method":"initialize"}`, wantID: `-1`},
		{name: "string id", request: `{"jsonrpc":"2.0","id":"client-session-123","method":"initialize"}`, wantID: `"client-session-123"`},
		{name: "null id", request: `{"jsonrpc":"2.0","id":null,"method":"initialize"}`, wantID: `null`},
		{name: "ping", request: `{"jsonrpc":"2.0","id":7,"method":"ping"}`, wantID: `7`, wantResult: `{}`},
		{name: "notification", request: `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{name: "unknown method", request: `{"jsonrpc":"2.0","id":8,"method":"no/such/method"}`, wantID: `8`, wantCode: -32601},
		{name: "parse error", request: `{"jsonrpc":"2.0","id":9,"method":`, wantID: `null`, wantCode: -32700},
		{name: "object id", request: `{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}`, wantID: `null`, wantCode: -32600},
		{name: "array id", request: `{"jsonrpc":"2.0","id":[1],"method":"ping"}`, wantID: `null`, wantCode: -32600},
		{name: "boolean id", request: `{"jsonrpc":"2.0","id":true,"method":"ping"}`, wantID: `null`, wantCode: -32600},
	} {
		t.Run(tc.name+"/stdio", func(t *testing.T) {
			var output bytes.Buffer
			if err := New(Options{}).ServeStdio(context.Background(), strings.NewReader(tc.request+"\n"), &output); err != nil {
				t.Fatal(err)
			}
			assertEnvelope(t, output.Bytes(), tc.wantID, tc.wantCode, tc.wantResult)
		})
		t.Run(tc.name+"/http", func(t *testing.T) {
			server := httptest.NewServer(New(Options{}).HTTPHandler())
			defer server.Close()
			response, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(tc.request))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			want := http.StatusOK
			if tc.wantID == "" {
				want = http.StatusAccepted
			}
			if response.StatusCode != want {
				t.Fatalf("HTTP %d, want %d: %s", response.StatusCode, want, body)
			}
			assertEnvelope(t, body, tc.wantID, tc.wantCode, tc.wantResult)
		})
	}
}

func assertEnvelope(t *testing.T, body []byte, wantID string, wantCode int, wantResult string) {
	t.Helper()
	if wantID == "" {
		if len(bytes.TrimSpace(body)) != 0 {
			t.Fatalf("notification answered with %s", body)
		}
		return
	}
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if string(response.ID) != wantID {
		t.Fatalf("id = %q, want %q: %s", string(response.ID), wantID, body)
	}
	switch {
	case wantCode == 0 && (response.Error != nil || len(response.Result) == 0):
		t.Fatalf("want a result: %s", body)
	case wantCode != 0 && (response.Error == nil || response.Error.Code != wantCode || len(response.Result) != 0):
		t.Fatalf("want error %d and no result: %s", wantCode, body)
	case wantResult != "" && string(response.Result) != wantResult:
		t.Fatalf("result = %s, want %s", response.Result, wantResult)
	}
}
