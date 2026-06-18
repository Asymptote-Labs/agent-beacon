# Asymptote TypeScript SDK

The Asymptote SDK instruments cloud-hosted agent apps through the `Observe`
module and exports OpenTelemetry spans that remain compatible with Beacon's
normalized JSONL event schema.

## Hosted Asymptote Observe

```bash
npm install @asymptote/sdk
export ASYMPTOTE_API_KEY=...
```

```typescript
import { Observe } from "@asymptote/sdk";

Observe.initialize({
  apiKey: process.env.ASYMPTOTE_API_KEY,
});
```

Hosted Observe contract:

- Transport: OpenTelemetry over HTTP.
- Default base URL: `https://api.asymptotelabs.ai`.
- Observe path: `/v1/observe` is appended when the endpoint does not already include it.
- Auth: `authorization: Bearer <ASYMPTOTE_API_KEY>`.
- Override base URL with `ASYMPTOTE_BASE_URL` or `initialize({ baseUrl })`.

The TypeScript SDK preserves this hosted contract, but this repository does not
implement the managed ingest service. Use `baseUrl` with a local stub in tests.

## Local Or Customer-Managed Observe Export

```typescript
import { Observe } from "@asymptote/sdk";

Observe.initialize({
  otlpEndpoint: process.env.OTEL_EXPORTER_OTLP_ENDPOINT,
});
```

The SDK appends `/v1/observe` when the endpoint does not already include it.
Cloud spans carry `beacon.origin=cloud` and the compatibility
attributes documented in `docs/asymptote-observe-sdk.md`.

Endpoint precedence:

1. `initialize({ otlpEndpoint })`
2. `OTEL_EXPORTER_OTLP_ENDPOINT`
3. hosted `baseUrl` when an API key is present

Beacon endpoint installs remain local-first and never require hosted credentials.

## Automatic AI Instrumentation

Asymptote Observe follows an OpenTelemetry-first pattern: initialize once
as early as possible, then use existing OpenLLMetry instrumentations for common
provider SDKs.

```typescript
import { Observe } from "@asymptote/sdk";

Observe.initialize({
  apiKey: process.env.ASYMPTOTE_API_KEY,
});
```

By default the SDK enables OpenAI and Anthropic OpenLLMetry instrumentations.
Disable or configure them if needed:

```typescript
Observe.initialize({
  apiKey: process.env.ASYMPTOTE_API_KEY,
  instrumentationOptions: {
    openAI: { traceContent: true },
    anthropic: false,
  },
});
```

If your app already owns OpenTelemetry setup, use
`Observe.instrumentations()` with that provider instead of calling
`initialize()` twice.

## Vercel AI SDK

```typescript
import { registerTelemetry } from "ai";
import { OpenTelemetry } from "@ai-sdk/otel";
import { Observe } from "@asymptote/sdk";

Observe.initialize();

registerTelemetry(new OpenTelemetry({
  tracer: Observe.getTracer(),
}));
```

## Claude Agent SDK

```typescript
import { query as originalQuery } from "@anthropic-ai/claude-agent-sdk";
import { Observe } from "@asymptote/sdk";

Observe.initialize();

const query = Observe.wrapClaudeAgentQuery(originalQuery);
```

For the first SDK release, prefer provider-level Anthropic/OpenLLMetry
instrumentation or `observe()` around the agent entry point. Custom Claude Agent
SDK hooks are deferred until those paths leave important telemetry gaps.

## Custom Agent Steps

```typescript
const plan = Observe.observe(
  {
    name: "agent.plan",
    attributes: {
      "beacon.harness.name": "custom_agent",
      "beacon.event.action": "tool.invoked",
    },
  },
  async (input: string) => {
    return runPlanner(input);
  },
);
```

Call `Observe.flush()` before short-lived scripts exit, or
`Observe.shutdown()` when the process owns the provider lifecycle.

## Token Usage Attribution

The OpenLLMetry instrumentations record `gen_ai.usage.*` token counts on every
supported model call, and the Beacon collector normalizes them into endpoint
JSONL for the token usage dashboard and `beacon token-usage` reports. Two
notes:

- Streaming responses report usage only after the stream is fully consumed;
  abandoning a stream early can drop the final usage record for that call.
- `traceContent` only controls prompt/completion text capture. Numeric token
  usage is always recorded.

To attribute usage to one agent session across spans, attach
`Observe.sessionAttributes(sessionId)` to your spans or resource attributes:

```typescript
const handle = Observe.observe(
  {
    name: "agent.handle_request",
    attributes: Observe.sessionAttributes(conversationId),
  },
  async (input: string) => runAgent(input),
);
```

## Beta Support Matrix

- OpenAI SDK: OpenLLMetry instrumentation enabled by default.
- Anthropic SDK: OpenLLMetry instrumentation enabled by default.
- Vercel AI SDK: register `@ai-sdk/otel` with `Observe.getTracer()`.
- Claude Agent SDK: wrap query functions with `wrapClaudeAgentQuery()` or use `observe()`.
- Custom orchestration: wrap functions with `observe()`.
- Custom Claude hook and OpenAI Agents tracing adapters are intentionally deferred.
