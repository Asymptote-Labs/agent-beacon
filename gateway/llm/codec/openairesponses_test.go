package codec

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

func TestResponsesRequest(t *testing.T) {
	req, err := openAIResponses{}.DecodeRequest(readFixture(t, "openairesponses_request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-5-codex" || !req.Stream {
		t.Errorf("model, stream = %q, %t", req.Model, req.Stream)
	}
	equalLines(t, "messages", describeMessages(req.Messages), []string{
		`system System text "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI."`,
		`system System text "<permissions instructions>sandbox_mode: workspace-write</permissions instructions>"`,
		`user User text "<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"`,
		`user User text "fix the failing test"`,
		`user User text "see screenshot"`,
		`user Media media image mime=image/jpeg url= data=/9j/4AAQ`,
		`user Media media file mime= url=file-xyz data=`,
		`user Media media file mime= url= data=JVBERi0=`,
		`assistant Assistant reasoning "**Inspecting tests**"`,
		`assistant Assistant reasoning "Running go test."`,
		`assistant Assistant call id=call_shell1 name=shell args={"command":["bash","-lc","go test ./..."],"workdir":"/repo"}`,
		`tool Result result call=call_shell1 error=false [text "FAIL: TestX\nexit status 1"]`,
		`assistant Assistant call id=call_patch1 name=apply_patch args=*** Begin Patch` + "\n" + `*** Update File: x.go` + "\n" + `@@` + "\n" + `-a` + "\n" + `+b` + "\n" + `*** End Patch`,
		`tool Result result call=call_patch1 error=false [text "Success. Updated the following files:\nM x.go"]`,
		`assistant Assistant call id=call_ls1 name=local_shell args={"type":"exec","command":["ls"],"env":{}}`,
		`tool Result result call=call_ls1 error=false [text "{\"output\":\"x.go\\n\",\"metadata\":{\"exit_code\":0}}"]`,
		`assistant Assistant call id=ws_1 name=web_search args={"type":"search","query":"go test flags"}`,
		`assistant Assistant call id=call_view1 name=mcp__github__get_issue args={"number":7}`,
		`tool Result result call=call_view1 error=false [text "screenshot:"; media image mime= url=https://example.com/s.png data=]`,
		`assistant Assistant call id=mcp_1 name=mcp__linear__get_ticket args={"id":"ENG-1"}`,
		`tool Result result call=mcp_1 error=false [text "ENG-1: flaky test"]`,
		`tool Result result call=call_cu1 error=false [media image mime=image/png url= data=iVBOR]`,
		`assistant Assistant text "Fixed."`,
	})
	equalLines(t, "tools", describeTools(req.Tools), []string{
		`shell kind=function desc="Runs a shell command" params={"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}},"required":["command"]}`,
		`apply_patch kind=custom desc="Edit files" params={"type":"grammar","syntax":"lark","definition":"start: begin_patch"}`,
		`mcp__github__get_issue kind=function desc="Get an issue" params={"type":"object"}`,
		`mcp__github__list_prs kind=function desc="List pull requests" params={"type":"object"}`,
		`web_search kind=web_search desc="" params=`,
		`linear kind=mcp desc="" params=`,
	})
	equalLines(t, "format", req.Format, []string{`{"verbosity":"low"}`, `"auto"`})
	equalLines(t, "unknown", req.Unknown, []string{"client_metadata", "input.item=compaction"})
}

func TestResponsesRequestStringInput(t *testing.T) {
	req, err := openAIResponses{}.DecodeRequest([]byte(`{"model":"gpt-5","input":"Tell me a joke","previous_response_id":"resp_9"}`))
	if err != nil {
		t.Fatal(err)
	}
	equalLines(t, "messages", describeMessages(req.Messages), []string{`user User text "Tell me a joke"`})
	if req.Unknown != nil {
		t.Errorf("unknown = %q, want none", req.Unknown)
	}
	if req.PreviousResponse != "resp_9" || req.Conversation != "" || !req.Stateful() {
		t.Errorf("previous %q conversation %q stateful %v", req.PreviousResponse, req.Conversation, req.Stateful())
	}
}

// The conversation a request continues is an id or an object naming one; null is none.
func TestResponsesRequestConversation(t *testing.T) {
	for body, want := range map[string]string{
		`{"input":"hi","conversation":"conv_1"}`:                   "conv_1",
		`{"input":"hi","conversation":{"id":"conv_2"}}`:            "conv_2",
		`{"input":"hi","conversation":null}`:                       "",
		`{"input":"hi","previous_response_id":null,"store":false}`: "",
	} {
		req, err := openAIResponses{}.DecodeRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if req.Conversation != want || req.Stateful() != (want != "") {
			t.Errorf("%s: conversation %q stateful %v, want %q", body, req.Conversation, req.Stateful(), want)
		}
		root, err := ReadRoot(llm.OpenAIResponses, []byte(body))
		if err != nil || root.Conversation != want || root.Stateful() != req.Stateful() || (root.First == nil) != req.Stateful() {
			t.Errorf("%s: ReadRoot = %+v, %v", body, root, err)
		}
	}
}

func TestResponsesResponse(t *testing.T) {
	const completed = `{"id":"resp_3","object":"response","model":"gpt-5-codex","status":"completed","error":null,"incomplete_details":null,` +
		`"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Plan"}]},` +
		`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done.","annotations":[]}]},` +
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}","status":"completed"}],` +
		`"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":64},"output_tokens_details":{"reasoning_tokens":5}}}`
	wantParts := []string{
		`assistant Assistant reasoning "Plan"`,
		`assistant Assistant text "Done."`,
		`assistant Assistant call id=call_1 name=shell args={"command":["ls"]}`,
	}
	wantUsage := llm.Usage{InputTokens: 100, OutputTokens: 10, CacheRead: 64}
	tests := []struct {
		name  string
		body  string
		id    string
		stop  string
		usage llm.Usage
		parts []string
	}{
		{name: "json", body: completed, id: "resp_3", stop: "completed", usage: wantUsage, parts: wantParts},
		{
			name: "json incomplete",
			body: `{"id":"resp_4","model":"gpt-5-codex","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Partial"}]}]}`,
			id:    "resp_4",
			stop:  "max_output_tokens",
			parts: []string{`assistant Assistant text "Partial"`},
		},
		{
			name: "sse with full terminal response",
			body: "event: response.created\n" +
				`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_3","model":"gpt-5-codex","status":"in_progress","output":[]}}` + "\n\n" +
				"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Done."}` + "\n\n" +
				"event: response.completed\n" +
				"data: " + `{"type":"response.completed","sequence_number":9,"response":` + completed + "}\n\n",
			id: "resp_3", stop: "completed", usage: wantUsage, parts: wantParts,
		},
		{
			// Codex's backend: the terminal response has an empty output, so the items come from
			// output_item.done events, ordered by output_index whatever order they finished in.
			name: "sse assembled from items",
			body: "event: response.created\n" +
				`data: {"type":"response.created","response":{"id":"resp_5","model":"gpt-5-codex","status":"in_progress","output":[]}}` + "\n\n" +
				"event: response.output_item.done\n" +
				`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_p","name":"apply_patch","input":"*** Begin Patch"}}` + "\n\n" +
				"event: response.output_item.done\n" +
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Plan"}],"encrypted_content":"gAAA"}}` + "\n\n" +
				"event: response.output_item.done\n" +
				`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"}}` + "\n\n" +
				"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"id":"resp_5","model":"gpt-5-codex","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":64}}}}` + "\n\n",
			id:    "resp_5",
			stop:  "completed",
			usage: wantUsage,
			parts: []string{
				`assistant Assistant reasoning "Plan"`,
				`assistant Assistant call id=call_1 name=shell args={"command":["ls"]}`,
				`assistant Assistant call id=call_p name=apply_patch args=*** Begin Patch`,
			},
		},
		{
			name: "sse cut before terminal event",
			body: "event: response.created\n" +
				`data: {"type":"response.created","response":{"id":"resp_6","model":"gpt-5-codex","status":"in_progress","output":[]}}` + "\n\n" +
				"event: response.output_item.done\n" +
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hal"}]}}`,
			id:    "resp_6",
			parts: []string{`assistant Assistant text "Hal"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkStream(t, llm.OpenAIResponses, []byte(tt.body))
			resp, err := openAIResponses{}.DecodeResponse([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if resp.ID != tt.id || resp.Model != "gpt-5-codex" || resp.StopReason != tt.stop {
				t.Errorf("id, model, stop = %q, %q, %q", resp.ID, resp.Model, resp.StopReason)
			}
			if resp.Usage != tt.usage {
				t.Errorf("usage = %+v, want %+v", resp.Usage, tt.usage)
			}
			equalLines(t, "parts", describeResponse(resp), tt.parts)
		})
	}
}

func TestResponsesResponseErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "stream error event",
			body: "event: response.created\n" +
				`data: {"type":"response.created","response":{"id":"resp_7","model":"gpt-5"}}` + "\n\n" +
				"event: error\n" +
				`data: {"type":"error","code":"rate_limit_exceeded","message":"Slow down","param":null}` + "\n\n",
			want: "rate_limit_exceeded: Slow down",
		},
		{
			name: "stream error event with nested error",
			body: "event: error\n" + `data: {"type":"error","error":{"type":"server_error","message":"boom"}}` + "\n\n",
			want: "server_error: boom",
		},
		{
			name: "error body",
			body: `{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error","param":"temperature","code":"unsupported_parameter"}}`,
			want: "Unsupported parameter",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkStream(t, llm.OpenAIResponses, []byte(tt.body))
			resp, err := openAIResponses{}.DecodeResponse([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) || resp != nil {
				t.Fatalf("DecodeResponse = %+v, %v; want error containing %q", resp, err, tt.want)
			}
		})
	}
}

// TestResponsesFailedResponse checks that a failed response is a response, not an error: the
// provider produced it and it may carry output.
func TestResponsesFailedResponse(t *testing.T) {
	resp, err := openAIResponses{}.DecodeResponse([]byte(
		`{"id":"resp_8","model":"gpt-5","status":"failed","error":{"code":"server_error","message":"The model failed"},"output":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "resp_8" || resp.StopReason != "failed" {
		t.Errorf("id, stop = %q, %q", resp.ID, resp.StopReason)
	}
}
