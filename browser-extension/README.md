# agent-beacon-browser-extension

Chrome (MV3) extension that relays LLM chat telemetry from the browser — Claude.ai and
ChatGPT — into the **same local Agent Beacon pipeline** as the endpoint agent, normalized into
the same schema. Browser chat activity (prompts, responses, tool calls) ends up in
`runtime.jsonl` next to agent activity, and forwards onward via the same Vector shippers.

It ships with a **fully automated, self-verifying build/test loop**: recorded chat streams are
replayed through the real, loaded extension in headless Chromium and asserted against — no login,
no API cost, deterministic.

## How it reaches Beacon

The extension never writes files (it can't — extensions are sandboxed). Instead its service
worker POSTs **OTLP GenAI logs** to the beacon collector already listening on
`http://127.0.0.1:4318/v1/logs`, which normalizes, redacts, rotates, and forwards them exactly
like every other source.

```
claude.ai / chatgpt.com page
  │  interceptor.js (MAIN world) — tees the streamed SSE via response.clone()
  ▼
content.js (ISOLATED world) — relays raw events to the service worker
  ▼
service worker — per-site adapter parses SSE → ChatTurn → normalize() → OTLP → batched POST
  ▼
http://127.0.0.1:4318/v1/logs  →  beacon-otelcol  →  runtime.jsonl  →  Vector → GCS/S3
```

## Setup

```bash
npm install
npx playwright install chromium   # one-time, for the e2e harness
```

## Commands

```bash
npm run build         # bundle src/ → dist/ (esbuild)
npm run build:watch   # rebuild on change
npm run typecheck     # tsc --noEmit
npm run test:unit     # pure adapter + normalization tests (vitest, no browser)
npm test              # builds dist/, runs the Playwright replay e2e (THE autonomous loop)
npm run test:headed   # watch it drive a browser
npm run test:integration   # opt-in: route through the real beacon-otelcol binary (Phase 4)
npm run test:live          # opt-in: headed smoke vs the real sites in an authed profile (Phase 4)
npm run report        # open the last HTML report
```

## Loading the extension in your own browser

`npm run build`, then in Chrome → Extensions → Developer mode → **Load unpacked** → select `dist/`.
Make sure the beacon endpoint agent is running (it provides the `127.0.0.1:4318` collector).

## Layout

| Path | Purpose |
|---|---|
| `src/shared/` | Site-agnostic core. `normalize.ts` (pure `ChatTurn → OTLP`), `otlp.ts`, `vocab.ts`, `types.ts`, `ids.ts`. |
| `src/adapters/` | The only site-specific code. `claude.ts` + `chatgpt.ts` (both done). `sse.ts` shared SSE framing. |
| `src/interceptor/` | MAIN-world `fetch`/stream tee. |
| `src/content/` | ISOLATED-world relay + (future) DOM fallback. |
| `src/background/` | Service worker: `assembler.ts`, `delivery.ts` (durable queue + backoff), `settings.ts`, `sw.ts`. |
| `src/popup/`, `src/options/` | On/off, retention toggle, endpoint + per-site config. |
| `e2e/` | Playwright fixtures + helpers (`mock-collector`, `sse-replay-server`, `otlp-assertions`) + specs. |
| `test/unit/` | vitest unit tests. |
| `fixtures/<site>/*.sse` | Recorded chat streams (currently **synthetic**, pending real capture). |

## Testing model (layered by fidelity/cost)

- **(a) unit** — recorded `.sse` → `ChatTurn` → golden OTLP. Milliseconds, no browser.
- **(b) e2e replay** — a local HTTPS server impersonates the chat site (via `--host-resolver-rules`
  mapping the real hostname to it + a throwaway self-signed cert), the real extension loads, and a
  **mock collector** captures what it POSTs. Fully autonomous — **this is the loop run every iteration.**
- **(c) integration** (opt-in) — route through the real `beacon-otelcol` binary into a temp
  `runtime.jsonl`; assert the actual `Event` schema. *(Phase 4)*
- **(d) live smoke** (opt-in, headed) — drive the real sites in a persistent authed profile; a
  drift alarm that flags when recorded fixtures go stale. *(Phase 4)*

## Status

This is a **V0 MVP**: Claude.ai capture proven end-to-end against the real site and the real
Beacon collector (prompt → response → tool calls → `runtime.jsonl` → S3 → dashboard).

- ✅ Shared core + normalization (unit-tested)
- ✅ Claude capture end-to-end + autonomous replay e2e
- ✅ Resilience: multi-tab correlation + aborted/partial stream (e2e)
- ✅ Content-script context-invalidation guard (survives extension reloads)
- ✅ ChatGPT capture (`delta_encoding: v1` parser) + autonomous replay e2e
- ⬜ ChatGPT stream-handoff/resume case (see limitations); DOM-fallback capture, XHR transport
- ⬜ Real-collector integration test + live-smoke/fixture-recorder as CI layers

### Known limitations (V0 — intentionally deferred)

- **Service-worker suspension mid-stream.** In-flight turn assembly lives in memory in the SW.
  Active streaming keeps the SW alive (each chunk is activity), so normal turns are safe; a >30s
  stall between chunks could drop a turn. The durable delivery queue (in `chrome.storage`) protects
  everything *after* a turn is assembled, not during. Persisting partial assembler state is a
  future item.
- **Delivery retry cadence.** Retries use `chrome.alarms`, which MV3 clamps to a ~30s minimum, so
  the first retry lands at ~30s rather than the computed sub-second backoff. Fine for a
  local-loopback collector that is almost always reachable; the queue guarantees eventual delivery.
- **Capture depends on `window.fetch`.** Confirmed correct for claude.ai. Sites using XHR or a page
  service worker for streaming would need additional interception (deferred).
- **Single capture path.** No DOM-scraping fallback yet; if a site changes its SSE wire format, the
  adapter needs updating (the live-smoke drift alarm is designed to catch this).
- **ChatGPT stream-handoff turns.** ChatGPT streams the answer as `delta_encoding: v1` SSE, usually
  *inline* on the `POST /backend-api/f/conversation` response (which we tee). Occasionally it instead
  returns a `stream_handoff` and streams the tokens on a **resume-SSE endpoint** — those turns are
  currently missed. That resume stream is also SSE-over-fetch (no WebSocket needed for tokens; the
  `wss://ws.chatgpt.com` socket only carries control messages), so teeing the resume GET is a
  follow-up, not a rearchitecture. ChatGPT web (like Claude web) doesn't stream token usage.
- **Telemetry integrity on the injected page.** The MAIN-world interceptor and any other page script
  share the same JS world, so the ISOLATED content script cannot cryptographically distinguish the
  extension's own `postMessage` events from forged ones. A malicious script already running on
  claude.ai could therefore inject fabricated chat records into the local collector. Impact is
  telemetry *integrity* only (no credential/code exposure). A proper fix is service-worker-driven
  MAIN injection (`chrome.scripting`) with a per-tab nonce the page can't read; deferred for V0.

> **Fixtures.** `fixtures/claude/simple-turn.sse` is a **real, sanitized** claude.ai capture
> (via `npm run record:fixtures`); the adapter is verified against it. `with-tool-call.sse` is
> still synthetic (written to the documented Anthropic tool-use shape) pending a real tool-call
> capture. Raw recorder output (`*.page.html`, `*.meta.json`, `real-*.sse`) is git-ignored because
> it can contain personal chat/account data — only hand-sanitized `*.sse` fixtures are committed.
>
> Notable real-format findings: claude.ai emits a leading `conversation_ready` event, nests the
> model in `message_start.message.model`, and does **not** stream token usage (so `gen_ai.usage.*`
> is absent for `claude_web`).
