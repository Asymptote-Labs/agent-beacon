// Package gateway is the contract between gateway adapters and the shared pipeline.
//
// An adapter translates one gateway's hook (a guardrail callout, an Envoy ext_proc stream, a log
// export) into Calls, hands each to a Handler, and translates the Verdict back into the gateway's
// wire format. That is all an adapter does: decoding provider protocols, sessions, analysis,
// policy and delivery belong to the pipeline, so an adapter stays a few hundred lines.
//
// Gateways expose three shapes of hook, and the contract covers each:
//
//   - Callout: the gateway calls out before and after the model with a normalized view and waits
//     for a verdict (LiteLLM, Portkey, TrueFoundry, agentgateway, Kong). The adapter fills
//     Request/Response itself, sets Normalized, and Enforce.
//   - Filter: the gateway streams the raw HTTP exchange through the adapter (Envoy ext_proc).
//     The adapter sets Protocol and Body and the pipeline decodes them; Enforce.
//   - Mirror: the gateway exports finished calls after the fact (log webhooks, OTLP). The adapter
//     delivers the request phase then the response phase, without Enforce; the verdict is
//     advisory.
package gateway

import (
	"context"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Phase is the point of the model call a Call describes.
type Phase uint8

// Phases.
const (
	// PhaseRequest is the request on its way to the provider.
	PhaseRequest Phase = iota + 1
	// PhaseResponse is the provider's reply on its way back to the client.
	PhaseResponse
)

func (p Phase) String() string {
	switch p {
	case PhaseRequest:
		return "request"
	case PhaseResponse:
		return "response"
	}
	return "unknown"
}

// Call is one phase of one model call, as a gateway saw it.
type Call struct {
	// Gateway names the adapter instance that saw the call ("litellm", "envoy-prod").
	Gateway string
	// ID is the gateway's id for the model call; both phases of a call carry the same one.
	ID    string
	Phase Phase
	// Enforce reports that the gateway acts on the verdict. A mirrored call has already
	// happened, so its verdict only records what policy would have decided.
	Enforce bool
	Time    time.Time
	Client  Client
	// Model is the model or route the client asked for, as the gateway names it.
	Model string

	// Content arrives in one of two forms. A filter adapter sets Protocol and Body to the bytes
	// on the wire, and the pipeline decodes them (a response Body may be a JSON reply or an SSE
	// stream). A callout or mirror adapter sets Request or Response to the gateway's own view and
	// sets Normalized.
	Protocol llm.Protocol
	Body     []byte
	Request  *llm.Request
	Response *llm.Response
	// Normalized reports that the content is the gateway's translation of what the client sent,
	// not the bytes themselves: fields the gateway does not translate are absent.
	Normalized bool
	// RequestBody is, on a response-phase call, the body of the request it answers, in Protocol,
	// when the adapter has it (Envoy holds both on one stream; Portkey's reply payload carries the
	// request). When nothing else names the call's session, the pipeline names the reply's from
	// it exactly as it named the request's, so the reply joins its request's session without
	// waiting for the request's analysis or asking the store.
	RequestBody []byte
}

// Client is what the gateway knows about who made the call.
type Client struct {
	UserAgent string
	// Headers are the request headers the gateway exposed, lowercase names. A gateway that
	// withholds a header's value must not appear to supply one: adapters drop placeholders.
	Headers map[string]string
	// Session is the gateway's own conversation or trace id, when it has one.
	Session  string
	Identity Identity
	Address  string
}

// Identity is the authenticated principal, as far as the gateway resolved it.
type Identity struct {
	User  string `json:"user,omitempty"`
	Email string `json:"email,omitempty"`
	Team  string `json:"team,omitempty"`
	Org   string `json:"org,omitempty"`
	// Key identifies the API key or virtual key (a hash or alias, never the secret).
	Key string `json:"key,omitempty"`
}

// Action is what the gateway should do with the call.
type Action uint8

// Actions.
const (
	Allow Action = iota
	Block
)

func (a Action) String() string {
	if a == Block {
		return "block"
	}
	return "allow"
}

// Verdict is the pipeline's answer for one Call.
type Verdict struct {
	Action Action
	// Reason is the message for the client when the call is blocked.
	Reason string
	// Rules are the ids of the findings that decided the verdict.
	Rules []string
}

// Handler processes calls. It never fails: when analysis cannot complete, the pipeline's policy
// decides the verdict, so an adapter always has an answer to give its gateway.
//
// An adapter calls Admit before it reads a call's content, with the most bytes it will read (the
// content's size, or the adapter's own limit when the size is not known; see BodySize), and
// Handle only if admitted. Admit reserves memory for the body: when the bodies being read and
// analyzed fill the pipeline's budget, it waits briefly, then refuses with the verdict the policy
// gives an overloaded call, and the adapter answers its gateway with that verdict at once,
// without reading the body. Handle in turn waits for capacity to analyze, and gives the same
// verdict when there is none. Memory and processors are rationed separately so that a call being
// uploaded never holds a processor, and neither lets calls queue until the gateway gives up on
// them. An admitted adapter calls release when Handle returns.
//
// Handle may answer before analysis ends, when nothing analysis finds could change the verdict;
// the pipeline then keeps reading the Call. An adapter hands over a Call and everything it points
// to (Body, Client.Headers, Request, Response) and never modifies or reuses them afterwards.
type Handler interface {
	Admit(ctx context.Context, size int64) (release func(), refused Verdict, ok bool)
	Handle(ctx context.Context, c *Call) Verdict
}

// Adapter connects one gateway to a Handler. Serve runs until ctx is cancelled or the adapter's
// listener fails, and returns nil on cancellation.
type Adapter interface {
	Name() string
	Serve(ctx context.Context, h Handler) error
}
