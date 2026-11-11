// Package portkey adapts Portkey AI Gateway's webhook guardrail (the `default.webhook` check) to
// the gateway contract.
//
// Portkey runs a guardrail's checks before the provider call (beforeRequestHook) and after it
// (afterRequestHook), POSTing the hook span's context to webhookURL:
//
//	{"request": {"json", "text", "isStreamingRequest", "isTransformed"},
//	 "response": {"json", "text", "statusCode", "isTransformed"},
//	 "provider", "requestType", "metadata", "eventType"}
//
// and reading {"verdict": bool} back (plugins/default/webhook.ts). request.json is the body the
// client sent, before Portkey translates it for the provider; response.json is Portkey's
// client-format reply (OpenAI chat for chatComplete, Anthropic for messages). Portkey deletes the
// client's headers from the payload, and metadata is the client's x-portkey-metadata header.
//
// Portkey only blocks on a false verdict when the guardrail is configured with deny: true; it
// then answers the client 446 with error.type "hooks_failed" and the hook results. A webhook that
// answers non-2xx or times out is an errored check, which Portkey treats as passing unless the
// check sets failOnError, so this adapter always answers 200 once it has a verdict.
package portkey

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"hash/crc32"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/gateway"
	"github.com/asymptote-labs/agent-beacon/gateway/internal/jsonscan"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Config configures the webhook listener.
type Config struct {
	Name string `yaml:"name"`
	Addr string `yaml:"addr"`
	Path string `yaml:"path"`
	// Token, when set, is required as "Authorization: Bearer <token>"; Portkey sends it through
	// the webhook check's headers parameter.
	Token string `yaml:"token"`
	// IdentityMetadata names the x-portkey-metadata keys that carry the caller's identity.
	IdentityMetadata IdentityMetadata `yaml:"identity_metadata"`
}

// IdentityMetadata maps identity fields to metadata keys; an empty key leaves the field unset.
type IdentityMetadata struct {
	User string `yaml:"user"`
	Team string `yaml:"team"`
	Org  string `yaml:"org"`
	Key  string `yaml:"key"`
}

// sessionKey is the metadata key read as the client's session id.
const sessionKey = "session_id"

// maxBody bounds a webhook payload; it carries the whole conversation and the reply.
const maxBody = 32 << 20

type adapter struct {
	cfg Config
	log *slog.Logger
}

// New validates cfg and applies defaults. Portkey's own `_user` metadata key is the default
// user identity, since Portkey documents it as the field for the end user.
func New(cfg Config, log *slog.Logger) (gateway.Adapter, error) {
	if cfg.Name == "" {
		cfg.Name = "portkey"
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:4022"
	}
	if cfg.Path == "" {
		cfg.Path = "/portkey/webhook"
	}
	if cfg.Path[0] != '/' {
		return nil, fmt.Errorf("portkey: path %q must start with /", cfg.Path)
	}
	if cfg.IdentityMetadata == (IdentityMetadata{}) {
		cfg.IdentityMetadata.User = "_user"
	}
	if log == nil {
		log = slog.Default()
	}
	return &adapter{cfg: cfg, log: log.With("gateway", cfg.Name)}, nil
}

func (a *adapter) Name() string { return a.cfg.Name }

func (a *adapter) Serve(ctx context.Context, h gateway.Handler) error {
	if err := gateway.ServeHTTP(ctx, a.cfg.Addr, a.handler(h), a.log.With("path", a.cfg.Path)); err != nil {
		return fmt.Errorf("portkey: %w", err)
	}
	return nil
}

// envelope is the webhook payload. Raw values keep request.json and response.json as the bytes
// Portkey sent, so the pipeline decodes exactly what Portkey saw.
type envelope struct {
	Request struct {
		JSON               jsontext.Value `json:"json"`
		IsStreamingRequest bool           `json:"isStreamingRequest"`
	} `json:"request"`
	Response struct {
		JSON jsontext.Value `json:"json"`
	} `json:"response"`
	RequestType string                    `json:"requestType"`
	Metadata    map[string]jsontext.Value `json:"metadata"`
	EventType   string                    `json:"eventType"`
}

// decodeOptions decode the envelope with encoding/json's (v1) semantics in one pass: v1's decoder
// scans the whole payload once to validate it before decoding, and the payload carries the whole
// conversation.
var decodeOptions = json.JoinOptions(jsonv1.DefaultOptionsV1(), jsonv1.ReportErrorsWithLegacySemantics(false))

type reply struct {
	Verdict bool `json:"verdict"`
	// Data is what Portkey reports back to the client as hook_results[].checks[].data.responseData.
	Data *replyData `json:"data,omitempty"`
}

type replyData struct {
	Reason string   `json:"reason,omitempty"`
	Rules  []string `json:"rules,omitempty"`
}

func (a *adapter) handler(h gateway.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+a.cfg.Path, func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Token != "" && !bearerMatches(r.Header.Get("Authorization"), a.cfg.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		release, refused, admitted := h.Admit(r.Context(), gateway.BodySize(r, maxBody))
		if !admitted {
			gateway.DiscardBody(r, maxBody)
			writeVerdict(w, refused)
			return
		}
		defer release()
		body, err := gateway.ReadBody(r, maxBody)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var env envelope
		if err := decodeEnvelope(body, &env); err != nil {
			http.Error(w, "bad webhook payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		c, ok := a.call(&env)
		if !ok {
			writeReply(w, reply{Verdict: true})
			return
		}
		writeVerdict(w, h.Handle(r.Context(), c))
	})
	return mux
}

// decodeEnvelope decodes the webhook payload in one pass over it. request.json and response.json
// are taken as bytes of the payload, neither decoded nor validated here (the codec validates what
// it reads); the other members the adapter reads are decoded as encoding/json (v1) decodes them,
// names matched case-insensitively and the last of duplicates winning. Decoding the whole payload
// validated and copied the conversation, and the reply, only for the codec to read them again.
func decodeEnvelope(body []byte, env *envelope) error {
	raws := [2]*jsontext.Value{&env.Request.JSON, &env.Response.JSON}
	phase := func(name string) int {
		switch {
		case strings.EqualFold(name, "request"):
			return 0
		case strings.EqualFold(name, "response"):
			return 1
		}
		return -1
	}
	add := func(buf []byte, name string, value []byte) []byte {
		if buf[len(buf)-1] != '{' {
			buf = append(buf, ',')
		}
		buf, _ = jsontext.AppendQuote(buf, name)
		return append(append(buf, ':'), value...)
	}
	small := append(make([]byte, 0, 1024), '{')
	var phases [2][]byte // the request and response objects' other members
	err := jsonscan.Members(body, func(name string) bool { return phase(name) >= 0 }, func(parent, name string, value []byte) error {
		if p := phase(parent); p >= 0 {
			if strings.EqualFold(name, "json") {
				*raws[p] = value
			} else if strings.EqualFold(name, "isStreamingRequest") {
				if phases[p] == nil {
					phases[p] = []byte{'{'}
				}
				phases[p] = add(phases[p], name, value)
			}
			return nil
		}
		// A request or response that is not an object (null, or a type error the decoder reports
		// as v1 would) arrives here too.
		if phase(name) >= 0 || strings.EqualFold(name, "requestType") || strings.EqualFold(name, "metadata") || strings.EqualFold(name, "eventType") {
			small = add(small, name, value)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for p, buf := range phases {
		if buf != nil {
			small = add(small, [2]string{"request", "response"}[p], append(buf, '}'))
		}
	}
	return json.Unmarshal(append(small, '}'), env, decodeOptions)
}

func writeVerdict(w http.ResponseWriter, v gateway.Verdict) {
	if v.Action != gateway.Block {
		writeReply(w, reply{Verdict: true})
		return
	}
	writeReply(w, reply{Data: &replyData{Reason: v.Reason, Rules: v.Rules}})
}

// call builds the Call for env, or reports false when there is nothing for the pipeline: a
// request type that is not model conversation (complete, embed), or the response phase of a
// streamed request, for which Portkey sends response.json null because it passes the stream
// through unparsed and cannot deny it anyway.
func (a *adapter) call(env *envelope) (*gateway.Call, bool) {
	var proto llm.Protocol
	switch env.RequestType {
	case "chatComplete":
		proto = llm.OpenAIChat
	case "messages":
		proto = llm.AnthropicMessages
	default:
		return nil, false
	}
	// Model is left to the pipeline, which reads it from the body it decodes anyway.
	c := &gateway.Call{
		Gateway:    a.cfg.Name,
		ID:         callID(env.Request.JSON),
		Enforce:    true,
		Time:       time.Now(),
		Protocol:   proto,
		Normalized: true,
		Client: gateway.Client{
			Session: metadata(env.Metadata, sessionKey),
			Identity: gateway.Identity{
				User: metadata(env.Metadata, a.cfg.IdentityMetadata.User),
				Team: metadata(env.Metadata, a.cfg.IdentityMetadata.Team),
				Org:  metadata(env.Metadata, a.cfg.IdentityMetadata.Org),
				Key:  metadata(env.Metadata, a.cfg.IdentityMetadata.Key),
			},
		},
	}
	switch env.EventType {
	case "beforeRequestHook":
		c.Phase = gateway.PhaseRequest
		c.Body = env.Request.JSON
	case "afterRequestHook":
		if isNull(env.Response.JSON) {
			return nil, false
		}
		c.Phase = gateway.PhaseResponse
		c.Body = env.Response.JSON
		// The reply's events carry the model the client asked for, as the request's do; the
		// reply names the provider's own model separately. The request it answers comes along,
		// so the reply's session is named from it as the request's was.
		c.Model = jsonscan.FirstString(env.Request.JSON, "model")
		if !isNull(env.Request.JSON) {
			c.RequestBody = env.Request.JSON
		}
	default:
		a.log.Warn("portkey: unknown eventType", "event_type", env.EventType)
		return nil, false
	}
	return c, true
}

// callID correlates the two phases of a call. Portkey's hook span id and trace id never reach
// the webhook, so the id is a digest of request.json, which Portkey serializes from the same object
// in both phases. Two calls with byte-identical bodies share an id, and a mutator guardrail that
// rewrites request.json before the response phase breaks the match. The digest needs to be the
// same on every replica, not unforgeable: a client that makes two of its own calls collide only
// confuses which of its reads were already reported. Two hardware CRCs and the length cost a
// fraction of a SHA-256 over a body of a megabyte.
func callID(requestJSON []byte) string {
	var sum [16]byte
	binary.BigEndian.PutUint32(sum[0:], crc32.Checksum(requestJSON, castagnoli))
	binary.BigEndian.PutUint32(sum[4:], crc32.ChecksumIEEE(requestJSON))
	binary.BigEndian.PutUint64(sum[8:], uint64(len(requestJSON)))
	return "portkey-" + hex.EncodeToString(sum[:])
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// metadata reads key from the client's metadata. Portkey types metadata as string values but
// passes through whatever JSON the client sent, so non-strings are kept as their JSON text.
func metadata(md map[string]jsontext.Value, key string) string {
	raw, ok := md[key]
	if key == "" || !ok || isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s, decodeOptions) == nil {
		return s
	}
	return string(raw)
}

func isNull(raw jsontext.Value) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func bearerMatches(header, token string) bool {
	const prefix = "Bearer "
	if len(header) < len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(token)) == 1
}

func writeReply(w http.ResponseWriter, r reply) {
	w.Header().Set("Content-Type", "application/json")
	_ = jsonv1.NewEncoder(w).Encode(r)
}
