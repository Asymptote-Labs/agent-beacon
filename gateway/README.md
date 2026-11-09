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
| A read's or an MCP tool's result | `file.read` / `mcp.tool_invoked`, with `gen_ai.tool.call.result` |
| Any other result | `tool.completed` (`tool.failed` for an error), keeping the call's target |

A gateway sees a tool call before the harness acts on it, so a call is recorded as the action it
asks for, and a rule can judge a read or a command before it happens. A read's or an MCP tool's
result repeats its call's action, which is where Beacon's rules read content from outside the
agent. When the gateway recorded the call, the result leaves out the target the call carried (the
path, or the MCP server and tool), so the two have different `event.id`s and a rule on the target
fires once; a result whose call it never saw keeps the target. Which calls it recorded is kept in
the store.

System text and tool definitions are resent on every call and recorded once per session. `model`
is the model the client asked for on every event of a call; `gen_ai.response.model` is the
provider's own name for it. The gateway's call id is in `raw["gateway.call_id"]`.

## Extension points

The pipeline has generic seams (`pipeline.Analyzer`, `pipeline.Policy`, `pipeline.Observer`,
`store.Store`). A separately distributed build can compile in files of its own that register
analyzers, a policy, stores and per-call observers in `cmd/beacon-gateway/extend.go`; each reads its
own node of the configuration file. The open build registers none: every call is allowed, and with
nothing that could change a verdict the pipeline answers each call as soon as it is admitted and
records it afterwards.

## Testing

```sh
cd gateway && go vet ./... && go test ./... && go test -race ./pipeline/... ./events/... ./litellm/... ./envoy/... ./portkey/... ./llm/...
go test ./internal/perf -run '^$' -bench . -benchmem
```
