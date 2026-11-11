# beacon-gateway

`gateway/` is a Go module that attaches Beacon to an LLM gateway. A thin adapter connects to the
gateway's hook, and one shared pipeline turns every model call it sees into Beacon events in
`runtime.jsonl`'s schema, with `harness.collection_method` set to `gateway`.

A gateway sees the conversation as the model receives it: the system prompt, the tool catalog, the
user's turns and tool results on the way in, and the model's text, token counts and tool calls on
the way out. It does not see the agent's own lifecycle, so its events complement the endpoint's
rather than replace them. Its `endpoint` is the gateway host and its `user` the identity the
gateway resolved.

## Adapters

| `type` | Hook | Shape |
|---|---|---|
| `litellm` | LiteLLM `generic_guardrail_api`, `pre_call` and `post_call` (`deploy/litellm.yaml`) | callout |
| `envoy` | Envoy External Processing (Envoy, Envoy Gateway, Istio; `deploy/envoy/envoy.yaml`) | filter |
| `portkey` | Portkey `default.webhook` guardrail | callout |

Protocols: Anthropic Messages, OpenAI Chat Completions and OpenAI Responses, JSON and SSE.

## Running

```sh
cd gateway
go build -o beacon-gateway ./cmd/beacon-gateway
./beacon-gateway -config deploy/beacon-gateway.yaml
```

Without `-config` it serves the LiteLLM callout on `127.0.0.1:4019` and writes
`gateway-events.jsonl` in the working directory. `/healthz` and `/stats` are served on the admin
listener (`127.0.0.1:4029`). `deploy/beacon-gateway.yaml` documents every setting; the shipped
configurations observe only (LiteLLM fails open, Envoy streams replies and lets calls through when
the gateway is down).

`content.retention: metadata` writes events as Beacon's Metadata-only forwarding does: identifiers,
names, paths, command lines, model and token usage, and no prompt, reply, system instructions, tool
arguments or results.

## Sessions

`session.id` is, in order: a session header the client sent (`pipeline.session_headers`, by default
`x-claude-code-session-id`, `session_id`, `x-session-id`, `conversation_id`); the gateway's own
conversation id (LiteLLM's trace id, Portkey's `session_id` metadata); or, failing both, a digest
of the conversation's root, `root:<hex>`: the gateway, the client's harness, the caller's identity,
the system text sent ahead of the messages (as written) and the first user message, which a client
resends unchanged on every call. A call's reply joins its request's session: Envoy and Portkey hand
the reply the request it answers, and the reply's session is named from it as the request's was
(LiteLLM names both by its trace id). A root needs an identity: without one (Envoy without
`identity_headers`, Portkey without identity metadata) different people's conversations that open
alike would be one session, so each call is its own session instead. Set identity headers to get
conversation-level sessions without a session header. The root is a heuristic: two conversations
of one caller in one harness with the same system text that open with the same message are one
session, and a client that changes its system text part-way through a conversation splits it into
two sessions there. System text sent after the messages (an Anthropic body may send it last) is
not part of the root, as reading it would mean reading past the whole history. Send a session
header, or Portkey `session_id` metadata, where that matters.

Naming a session reads no store: a call's two phases are named by the same function from the same
request body, so they always agree, in any order and on any replica.

A resent tail is taken for a client's retry, analyzed but not recorded again, only in a session
the client or the gateway named: a session header, the gateway's conversation id, a provider
conversation id. In a root session or a call's own session a retry is recorded twice, and two
conversations that share a root never lose either one's prompts, only share its `session.id`.

An OpenAI Responses request that continues provider-side state carries only its new turn, so it
has no root. With `conversation` set, the conversation id is the session. A request that continues
`previous_response_id`, with no session header naming it, is its own session, `<gateway>:<call
id>`, which its reply shares. Every event of the call names the response it continues in
`raw["gateway.previous_response_id"]`, and the reply's events carry its own response id in
`gen_ai.response.id`, so a reader can put such a chain's turns back together. Such a request's
new turn is recorded every time: the same short input twice ("yes", "go on") is two turns, never
a retry.

The cost of a turn being its own session: the Responses API does not carry `instructions` or
`tools` across responses, so the client resends them every turn, and each turn records them again
(`session.context`, `mcp.tool_listed`) and has its own `session.id`. This is deliberate. Naming
such a turn's session from content (the caller and its instructions) would merge every
conversation one caller has with the same instructions, and naming it from the response it
continues would need state shared across calls and replicas. A client that chains responses this
way and wants one session per conversation, with its context recorded once, sends a session
header.

## Events

| What the call carries | Event |
|---|---|
| New system text | `session.context` (`gen_ai.system_instructions`) |
| New MCP tool definition | `mcp.tool_listed` |
| New user text | `prompt.submitted` |
| The model's text | `agent.message` (`gen_ai.output.messages`, `gen_ai.response`) |
| The reply's token counts | `token.usage` (`gen_ai.usage`, `gen_ai.response`) |
| A shell tool call | `command.executed` |
| A read tool call | `file.read`, with `file.path` |
| An MCP tool call | `mcp.tool_invoked`, with `mcp.server` and `mcp.tool` |
| A fetch or any other tool call | `tool.invoked` (`tool.path` is a fetch's URL) |
| A read's or an MCP tool's result | `file.read` / `mcp.tool_invoked`, with `gen_ai.tool.call.result` and the call's arguments |
| Any other result | `tool.completed` (`tool.failed` for an error), keeping the call's target |

A gateway sees a tool call before the harness acts on it, so a call is recorded as the action it
asks for, and a rule can judge a read or a command before it happens. A read's or an MCP tool's
result repeats its call's action, which is where Beacon's rules read content from outside the
agent. The result never carries the target the call carried (the path, or the MCP server and
tool): a rule on the target fires once, on the call, and a call and its result never share an
`event.id`, whichever replica saw the call. The call's arguments are on the result's
`gen_ai.tool.call.arguments`, so a reader can still see what it answers.

System text and tool definitions are resent on every call and recorded once per session, and so is
a request's new tail (its user text and tool results) in a session the client or the gateway
named: a client that retries a request sends the same tail, which is analyzed again but not
recorded again (see Sessions). `model` is the model the client asked
for on every event of a call; `gen_ai.response.model` is the provider's own name for it. The
gateway's call id is in `raw["gateway.call_id"]`, and the response a Responses call continues in
`raw["gateway.previous_response_id"]` (on a reply, when no session header names it).

## Extension points

The pipeline has generic seams (`pipeline.Analyzer`, `pipeline.Policy`, `pipeline.Observer`,
`pipeline.Sink`, `store.Store`). A separately distributed build can compile in files of its own that
register analyzers, a policy, stores, per-call observers and outputs in
`cmd/beacon-gateway/extend.go`; each reads its own node of the configuration file. The open build
registers none beyond the in-memory store and the `jsonl` output: every call is allowed, and with
nothing that could change a verdict the pipeline answers each call as soon as it is admitted and
records it afterwards.

An output is an entry of `outputs`, naming its type and the record kinds it receives:

```yaml
outputs:
  - jsonl: /var/log/beacon/gateway-events.jsonl   # shorthand for type: jsonl
    kinds: [beacon.event]
  - type: <a registered output>                     # its own keys alongside
    kinds: [beacon.event, finding]
```

A sink receives its kinds in batches, off the call path; when it falls behind, records are dropped
and counted in `/stats`.

## Testing

```sh
cd gateway && go vet ./... && go test ./... && go test -race ./pipeline/... ./events/... ./litellm/... ./envoy/... ./portkey/... ./llm/...
go test ./internal/perf -run '^$' -bench . -benchmem
```
