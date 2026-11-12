// Package litellm adapts LiteLLM's Generic Guardrail API (guardrail: generic_guardrail_api), a
// callout: LiteLLM POSTs to <api_base>/beta/litellm_basic_guardrail_api once before the model
// call (input_type "request") and once after it ("response"), and waits for a verdict.
//
// LiteLLM normalizes every protocol it serves (Anthropic Messages, OpenAI Responses, Chat
// Completions) into OpenAI-chat-shaped messages and tools before calling out, so the adapter
// reassembles them into a Chat Completions body and lets the pipeline's chat codec decode it.
package litellm

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/jsonscan"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Path is where LiteLLM posts guardrail calls under the configured api_base.
const Path = "/beta/litellm_basic_guardrail_api"

// withheld is the value LiteLLM sends for a header not on the guardrail's extra_headers
// allowlist: the header existed, its value is withheld.
const withheld = "[present]"

// maxBody bounds a guardrail payload; a long session with images is large.
const maxBody = 64 << 20

// Config configures the adapter.
type Config struct {
	Name string `yaml:"name"`
	Addr string `yaml:"addr"`
}

type adapter struct {
	cfg Config
	log *slog.Logger
}

// New returns the adapter.
func New(cfg Config, log *slog.Logger) (gateway.Adapter, error) {
	if cfg.Name == "" {
		cfg.Name = "litellm"
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:4019"
	}
	return &adapter{cfg: cfg, log: log}, nil
}

func (a *adapter) Name() string { return a.cfg.Name }

func (a *adapter) Serve(ctx context.Context, h gateway.Handler) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, func(w http.ResponseWriter, r *http.Request) { a.guardrail(w, r, h) })
	return gateway.ServeHTTP(ctx, a.cfg.Addr, mux, a.log.With("gateway", a.cfg.Name))
}

// request is LiteLLM's guardrail payload; only the fields the adapter reads are declared. The
// conversation fields stay raw JSON: they are spliced into the body the pipeline decodes.
type request struct {
	InputType          string         `json:"input_type"`
	CallID             string         `json:"litellm_call_id"`
	TraceID            string         `json:"litellm_trace_id"`
	Model              string         `json:"model"`
	StructuredMessages jsontext.Value `json:"structured_messages"`
	Texts              jsontext.Value `json:"texts"`
	Tools              jsontext.Value `json:"tools"`
	ToolCalls          jsontext.Value `json:"tool_calls"`
	// messages are StructuredMessages' elements, as the walk of the payload split them.
	messages       [][]byte
	RequestHeaders map[string]string `json:"request_headers"`
	RequestData    struct {
		KeyHash   string `json:"user_api_key_hash"`
		KeyAlias  string `json:"user_api_key_alias"`
		UserID    string `json:"user_api_key_user_id"`
		UserEmail string `json:"user_api_key_user_email"`
		TeamID    string `json:"user_api_key_team_id"`
		OrgID     string `json:"user_api_key_org_id"`
	} `json:"request_data"`
}

// decodeOptions decode the payload as encoding/json (v1) does, without its legacy error
// reporting, which scans the whole payload once more to validate it before decoding.
var decodeOptions = json.JoinOptions(jsonv1.DefaultOptionsV1(), jsonv1.ReportErrorsWithLegacySemantics(false))

type response struct {
	Action        string `json:"action"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

func (a *adapter) guardrail(w http.ResponseWriter, r *http.Request, h gateway.Handler) {
	release, refused, ok := h.Admit(r.Context(), gateway.BodySize(r, maxBody))
	if !ok {
		gateway.DiscardBody(r, maxBody)
		writeVerdict(w, refused)
		return
	}
	defer release()
	body, free, err := gateway.ReadBody(r, maxBody)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The pipeline returns the body's buffer once it has read the call (Call.Release); a payload
	// that never reaches it is returned here, after anything that may still format part of it.
	handed := false
	defer func() {
		if !handed {
			free()
		}
	}()
	var req request
	if err := decodeRequest(body, &req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	c, err := a.call(req, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A request built over its payload keeps the payload until the pipeline has read it. Any other
	// body (a reply, a texts-only request, one the payload had no room for) is a copy and refers
	// to none of the payload, which goes back to the pool at once.
	if _, ok := sliceOf(body, c.Body); ok {
		c.Release, c.BodyBytes = free, int64(cap(body))
	} else {
		free()
		c.BodyBytes = int64(cap(c.Body) + cap(c.RequestBody))
	}
	handed = true
	writeVerdict(w, h.Handle(r.Context(), c))
}

// decodeRequest decodes LiteLLM's payload. The conversation members are taken as bytes of the
// payload, neither decoded nor validated here: the codec validates what it reads, and a payload
// whose conversation does not decode is a failed analysis like any undecodable call.
// The other members the adapter reads are decoded as encoding/json (v1) decodes them, names
// matched case-insensitively and the last of duplicates winning. Decoding the whole payload
// validated and copied a long session's history, and a second copy of its text (texts), on every
// call, only for the codec to read the history again. The walk splits the messages into their
// elements as it passes them, which the codec takes rather than walking them again.
func decodeRequest(body []byte, req *request) error {
	small := append(make([]byte, 0, 1024), '{')
	isMessages := func(name string) bool { return strings.EqualFold(name, "structured_messages") }
	split := func(array bool, path ...string) bool { return array && len(path) == 1 && isMessages(path[0]) }
	err := jsonscan.Split(body, nil, split, func(_, name string, value []byte, elems [][]byte) error {
		switch {
		case isMessages(name):
			req.StructuredMessages, req.messages = value, elems
		case strings.EqualFold(name, "texts"):
			req.Texts = value
		case strings.EqualFold(name, "tools"):
			req.Tools = value
		case strings.EqualFold(name, "tool_calls"):
			req.ToolCalls = value
		case slices.ContainsFunc(smallMembers, func(m string) bool { return strings.EqualFold(name, m) }):
			if len(small) > 1 {
				small = append(small, ',')
			}
			small, _ = jsontext.AppendQuote(small, name)
			small = append(append(small, ':'), value...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(append(small, '}'), req, decodeOptions)
}

// smallMembers are the payload members decodeRequest decodes.
var smallMembers = []string{"input_type", "litellm_call_id", "litellm_trace_id", "model", "request_headers", "request_data"}

func writeVerdict(w http.ResponseWriter, v gateway.Verdict) {
	resp := response{Action: "NONE"}
	if v.Action == gateway.Block {
		resp = response{Action: "BLOCKED", BlockedReason: v.Reason}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonv1.NewEncoder(w).Encode(resp)
}

// call builds the call from the decoded payload; a request's body is built over the payload's
// buffer (requestBody), so nothing may read req's raw members, or the payload, afterwards.
func (a *adapter) call(req request, payload []byte) (*gateway.Call, error) {
	rd := req.RequestData
	c := &gateway.Call{
		Gateway:    a.cfg.Name,
		ID:         req.CallID,
		Enforce:    true,
		Model:      req.Model,
		Protocol:   llm.OpenAIChat,
		Normalized: true,
		Client: gateway.Client{
			Headers: gateway.ClientHeaders(req.RequestHeaders, withheld),
			Session: req.TraceID,
			Identity: gateway.Identity{
				User:  rd.UserID,
				Email: rd.UserEmail,
				Team:  rd.TeamID,
				Org:   rd.OrgID,
				Key:   firstNonEmpty(rd.KeyAlias, rd.KeyHash),
			},
		},
	}
	// From the filtered headers: names lowercased, and a value LiteLLM withheld ("[present]") is
	// not taken for the user agent.
	c.Client.UserAgent = c.Client.Headers["user-agent"]
	var err error
	switch req.InputType {
	case "request":
		c.Phase = gateway.PhaseRequest
		c.Body, c.BodyMessages, err = requestBody(req, payload)
	case "response":
		c.Phase = gateway.PhaseResponse
		c.Body, err = responseBody(req)
		// LiteLLM names the session by its trace id on both phases, and the pipeline reads a
		// reply's request only when nothing names its session: a reply payload that also carries
		// the request's messages hands them on only for a reply without a trace id, rather than
		// copying the whole conversation into a body nothing reads.
		if err == nil && req.TraceID == "" && nullable(req.StructuredMessages) != nil {
			c.RequestBody, _, err = requestBody(req, nil)
		}
	default:
		return nil, errors.New("unknown input_type " + req.InputType)
	}
	return c, err
}

// requestBody reassembles a Chat Completions request from LiteLLM's normalized messages and
// tools, spliced in as sent: each is one bracket-matched JSON value (jsonscan checks that much),
// the codec validates the body it decodes, and re-encoding them would only scan and copy a long
// session's history once more. A payload without structured messages (a texts-only guardrail
// call) becomes user turns.
//
// payload, when not nil, is the buffer req's members were read from, which nothing reads once the
// request is built: the request is built over it (inPlace) rather than copied into a second buffer
// the size of the conversation, and the messages' elements, which do not move, are returned as
// the walk split them, for the codec. A copied request returns none.
func requestBody(req request, payload []byte) (body []byte, messages [][]byte, err error) {
	msgs := nullable(req.StructuredMessages)
	tools := nullable(req.Tools)
	if msgs != nil && payload != nil {
		if b, ok := inPlace(payload, req.Model, msgs, tools); ok {
			return b, req.messages, nil
		}
	}
	if msgs == nil {
		texts, err := decodeTexts(req.Texts)
		if err != nil {
			return nil, nil, err
		}
		msgs = append(msgs, '[')
		for i, t := range texts {
			if i > 0 {
				msgs = append(msgs, ',')
			}
			msgs = append(msgs, `{"role":"user","content":`...)
			msgs, _ = jsontext.AppendQuote(msgs, t)
			msgs = append(msgs, '}')
		}
		msgs = append(msgs, ']')
	}
	b := make([]byte, 0, len(`{"model":"","messages":,"tools":}`)+len(req.Model)+len(msgs)+len(tools))
	b = append(b, '{')
	if req.Model != "" {
		b = append(b, `"model":`...)
		b, _ = jsontext.AppendQuote(b, req.Model)
		b = append(b, ',')
	}
	b = append(b, `"messages":`...)
	b = append(b, msgs...)
	if tools != nil {
		b = append(b, `,"tools":`...)
		b = append(b, tools...)
	}
	return append(b, '}'), nil, nil
}

// messagesKey opens a request built over its payload, written over the end of the longer name the
// payload gives the messages ("structured_messages", in any spelling) just before them.
const messagesKey = `{"messages":`

// inPlace builds requestBody's request over payload, the buffer msgs and tools (nil when absent)
// are slices of, without moving the messages, a long session's history:
// {"messages":…,"tools":…,"model":…}, opened over the end of the messages' name and closed after
// them, where the tools are moved from wherever they were and the model written from its decoded
// copy. Every position is checked before anything is written: the opening must not reach the
// tools, tools after the messages must start no earlier than where they go, and the request must
// fit the payload. When one does not hold it reports false with payload untouched.
func inPlace(payload []byte, model string, msgs, tools []byte) ([]byte, bool) {
	ms, ok := sliceOf(payload, msgs)
	if !ok || ms < len(messagesKey) {
		return nil, false
	}
	start, me := ms-len(messagesKey), ms+len(msgs)
	end := me
	var ts int
	if tools != nil {
		if ts, ok = sliceOf(payload, tools); !ok {
			return nil, false
		}
		if ts < ms && ts+len(tools) > start {
			return nil, false // the opening would overwrite tools before the messages
		}
		if end += len(`,"tools":`); ts > ms && end > ts {
			return nil, false // tools after the messages would be overwritten before they move
		}
		end += len(tools)
	}
	var quoted []byte
	if model != "" {
		quoted, _ = jsontext.AppendQuote(nil, model)
		end += len(`,"model":`) + len(quoted)
	}
	if end++; end > len(payload) {
		return nil, false
	}
	copy(payload[start:], messagesKey)
	w := me
	if tools != nil {
		w += copy(payload[w:], `,"tools":`)
		w += copy(payload[w:], payload[ts:ts+len(tools)]) // copy moves overlapping bytes correctly
	}
	if quoted != nil {
		w += copy(payload[w:], `,"model":`)
		w += copy(payload[w:], quoted)
	}
	w += copy(payload[w:], "}")
	return payload[start:w], true
}

// sliceOf returns where s starts in b when s is a non-empty slice of b's bytes.
func sliceOf(b, s []byte) (int, bool) {
	at := cap(b) - cap(s)
	if len(s) == 0 || at < 0 || at+len(s) > len(b) || &b[at] != &s[0] {
		return 0, false
	}
	return at, true
}

// responseBody reassembles a Chat Completions reply: LiteLLM sends the reply's text and its tool
// calls.
func responseBody(req request) ([]byte, error) {
	texts, err := decodeTexts(req.Texts)
	if err != nil {
		return nil, err
	}
	type message struct {
		Role      string         `json:"role"`
		Content   string         `json:"content"`
		ToolCalls jsontext.Value `json:"tool_calls,omitzero"`
	}
	type choice struct {
		Message message `json:"message"`
	}
	return json.Marshal(struct {
		Model   string   `json:"model,omitempty"`
		Choices []choice `json:"choices"`
	}{req.Model, []choice{{message{"assistant", strings.Join(texts, "\n"), nullable(req.ToolCalls)}}}})
}

// decodeTexts decodes the texts field; a request with structured messages never reads it.
func decodeTexts(raw jsontext.Value) ([]string, error) {
	var texts []string
	if raw == nil {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &texts, decodeOptions); err != nil {
		return nil, errors.New("decode texts: " + err.Error())
	}
	return texts, nil
}

// nullable is raw, or nil when it is absent or null.
func nullable(raw jsontext.Value) jsontext.Value {
	if len(raw) == 0 || raw.Kind() == 'n' {
		return nil
	}
	return raw
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
