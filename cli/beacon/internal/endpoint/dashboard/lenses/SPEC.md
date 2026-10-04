# Lenses — Specification (v1)

A lens is a single HTML file that renders one Beacon trace. The local dashboard
(`beacon endpoint dashboard`) shows lenses as tabs next to the full trace view.
Each tab reads the same trace and answers a different question: which files
changed, what the run cost, what the threat rules flagged.

A lens runs in a sandboxed frame with no network and no access to the dashboard.
It receives the trace through one call, `window.beacon.getTrace()`. Everything
else is plain HTML, CSS and JavaScript, so any coding agent can write one from
this document.

- Spec version: `lenses/v1` (see `VERSION` and [Versioning](#versioning)).
- Data contract: `beacon.lens.v1`, the Go types `LensDataV1` and
  `LensManifestV1` in `pkg/asymptoteobserve/lens.go`.
- Manifest schema: `manifest.schema.json` (JSON Schema, draft 2020-12).
- Examples: `examples/activity.lens.html` (a complete lens) and
  `examples/lens-data.json` (what `getTrace()` resolves with). A test pins
  both against the Go types.

## The lens file

A lens is one UTF-8 HTML document of at most **16 MiB**. All CSS, JavaScript,
images and fonts are inline (`data:` URIs for images and fonts). Nothing is
loaded from anywhere else.

The file carries its own manifest in exactly one element whose opening tag is
spelled exactly like this:

```html
<script type="application/beacon-lens+json">
{
  "id": "files-changed",
  "title": "Files Changed",
  "description": "Every file the agent touched, with its diffs.",
  "icon": "file",
  "version": 1,
  "api": "beacon.lens.v1"
}
</script>
```

Readers find the manifest by that literal opening tag, without an HTML parser,
so do not add attributes or change quoting or case. Browsers do not run a
script of an unknown type, so the manifest is inert. If a string in it
contains `<`, write it as `<` so the text `</script>` cannot occur early.

| Field | Required | Type | Meaning |
|---|---|---|---|
| `id` | yes | `^[a-z0-9][a-z0-9-]{0,62}$` | Stable identity. Unique on the endpoint. Built-in lens IDs are reserved. |
| `title` | yes | string, 1–60 chars, one line | Tab label. |
| `description` | no | string, ≤ 280 chars, one line | Shown in the lens picker. |
| `icon` | no | `^[a-z][a-z0-9-]{0,31}$` | One generic word (`file`, `shield`, `coins`, `list`, …). An unknown word gets the default icon. |
| `version` | yes | int ≥ 1 | Content revision; bump on every change. |
| `api` | yes | `"beacon.lens.v1"` | The data contract the lens was written against. A lens naming another value is not run. |
| `author` | no | string, ≤ 100 chars, one line | Attribution. |

Unknown fields are an error, so a misspelled key fails lint instead of being
ignored.

## Getting the trace

```js
const data = await window.beacon.getTrace();
```

- The host defines `window.beacon` before any lens script runs.
- `getTrace()` returns a promise. Calling it again returns the same promise;
  the data is computed once per load and never refreshed.
- It resolves within **10 seconds** or rejects with an `Error`. If the lens
  has not loaded the trace by then, the host shows the full trace view.
- There are no other calls. A lens cannot ask for more events, another trace,
  or anything from the network.

### `LensDataV1`

```jsonc
{
  "api_version": "beacon.lens.v1",
  "trace": { /* TraceBundleV1: schema_version "beacon.trace.v1" */ },
  "findings": {                 // absent: the trace was not scanned
    "rules_evaluated": 42,
    "items": [ /* LensFindingV1 */ ],
    "skipped": ["rule-needing-a-newer-spec"]
  },
  "token_coverage": {           // absent: unknown
    "harness": "claude_code",
    "status": "covered",        // covered | silent | inactive | not_instrumented
    "expectation": "reported",  // reported | generic_otlp | none
    "reason": "OTLP token and cost telemetry"
  },
  "token_usage": {              // absent: unknown
    "totals": { "input_tokens": 18234, "output_tokens": 1650, "cost_usd": 0.0912 },
    "events_with_usage": 1,
    "by_model": [{ "model": "claude-sonnet-5-5", "usage": { /* same shape */ }, "events": 1 }]
  },
  "truncated": false
}
```

`trace` is the existing trace bundle, the same projection the dashboard's
trace view uses. Its main parts:

- `trace.summary`: title, `started_at`/`ended_at`, `event_count`, `harness`
  (`name`, `version`, `collection_methods`), `session`, `repository`,
  `model`, `token_usage`, and `content.retention`.
- `trace.events[]`, in order. Each event has `id`, `number` (1-based
  position), `timestamp`, `type`, `action`, and optional `category`,
  `fidelity` (`observed` | `inferred`), `actor`, `title`, `summary`,
  `content`, `tool`, `command`, `file`, `mcp`, `approval`, `model`, `usage`,
  `tool_call_id` and `trace` (span IDs).
- `type` is one of `user_message`, `agent_message`, `agent_reasoning`,
  `tool_call`, `tool_result`, `command`, `file`, `mcp`, `approval`,
  `token_usage`, `session`, `error` or `other`. The list may grow (see
  [Robustness](#robustness)).
- Retained text arrives as a content object: `{text?, json?, retention,
  included, redacted?, truncated?, hash?, bytes?}`. When `included` is false
  the text was not retained or was redacted; `hash` and `bytes` still
  identify it. Command output and file diffs use the same object
  (`command.output`, `file.diff`).
- `trace.spans[]` and `trace.range` (`total_events`, `returned_events`).

`findings.items[]` are the active threat rules' matches over this trace's
own events: `rule_id`, `title`, `severity` (`info` … `critical`), `posture`,
`reason`, `taxonomy`, and `event_ids`. The evidence is referenced by
`trace.events[].id`, not copied. Findings, `token_coverage` and
`token_usage` are computed from the live runtime log. For a trace whose lines
have been rotated out of that log (and are kept only in the opt-in trace
history), they may be missing or partial while `trace` is still complete.

`token_usage` is the trace's usage as `beacon token-usage` counts it, with
duplicate channels removed and cumulative counters turned into deltas. Each
event's own `usage` is what that event reported, so summing those can count
the same tokens twice; use `token_usage.totals` for the total, and say so if
you show both. `cost_usd` is only ever what the runtime reported; Beacon never
estimates cost.

`truncated` is true when the bundle holds fewer events than the trace has,
because it hit the size cap. A finding's evidence ID may then point at an
event that is not in the bundle.

The full field list is the Go source in `pkg/asymptoteobserve/trace.go` and
`lens.go`. `examples/lens-data.json` is a complete instance.

### Content and privacy

A lens sees whatever the local log retained. With `full` retention that
includes prompts, responses, command output and diffs, after Beacon's local
redaction. A lens can show that content on screen and nowhere else: the
sandbox below gives it no way to send it anywhere. Forwarding privacy modes
(such as metadata-only) govern what leaves the machine. They do not reduce
what a local lens receives.

## Sandbox

The host enforces these rules; a lens does not opt into them.

- The frame is `<iframe sandbox="allow-scripts">`, without
  `allow-same-origin`. The lens runs in an opaque origin and cannot read the
  dashboard, its cookies, its storage or its API. It also cannot navigate the
  page, open popups or submit forms.
- A lens must not navigate its own frame. The dashboard's policy refuses any
  frame navigation off its origin (including `data:` and `blob:` URLs), and
  the host closes a lens whose frame loads a second document, without giving
  that document a port.
- The frame is served with this Content Security Policy:

  ```
  default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline';
  img-src data: blob:; font-src data:; media-src data: blob:;
  connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'
  ```

  No `fetch`, XHR, WebSocket, `EventSource`, remote image, remote font,
  worker, nested frame, `eval` or `new Function`. Load nothing by URL.
- Trace data reaches the frame only over a private `MessageChannel` port. The
  one `postMessage` from the host to the window carries that port and
  nothing else. The lens never uses `window.postMessage` itself;
  `window.beacon` handles the port.
- `localStorage`, `sessionStorage`, IndexedDB and cookies are unavailable in
  an opaque origin. Keep state in memory.
- If the lens throws before rendering, rejects, or does not render within
  the timeout, the host falls back to the full trace view. A broken lens
  cannot break the dashboard.

## Layout and style

- **Height.** The host sizes the frame to the document's height and keeps it
  updated as the content changes. Do not use `100vh` or `height: 100%` on the
  root; let the page grow.
- **Width.** Use the full frame width, with no centred `max-width` and no outer
  padding on `html` or `body`. Do not put a background on the root; the frame
  is transparent over the dashboard.
- **Tokens.** Before the lens's own styles, the host defines these custom
  properties on `:root`. Use them with a fallback so the file also renders on
  its own:

  | Token | Value |
  |---|---|
  | `--beacon-text` | `#111827` |
  | `--beacon-muted` | `#6b7280` |
  | `--beacon-line` | `rgba(0, 0, 0, 0.1)` |
  | `--beacon-panel-soft` | `#f9fafb` |
  | `--beacon-accent` | `#036aa2` |
  | `--beacon-accent-soft` | `#e2f0f8` |
  | `--beacon-danger` | `#b91c1c` |
  | `--beacon-warn` | `#92400e` |
  | `--beacon-ok` | `#047857` |
  | `--beacon-font-sans` | the dashboard's sans stack |
  | `--beacon-font-mono` | `ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace` |

- **Type.** 13–14px body at weight 400; monospace at 13px for code, paths and
  commands. Use as few type sizes and spacing values as you can.
- **Density.** Prefer compact tables and tight rows over padded cards. Use
  hairline borders (`--beacon-line`) rather than shadows. Right-align numbers
  and format them with `Intl.NumberFormat`. Keep animation under 150 ms.
- **Colour.** Use `--beacon-accent` for links and focus, and `--beacon-danger`,
  `--beacon-warn` and `--beacon-ok` only where they mean something.

## Robustness

- Render a loading state immediately, then fill it in when `getTrace()`
  resolves. On rejection, show the error message.
- Only these are guaranteed present: `api_version`, `trace.schema_version`,
  `trace.id`, `trace.summary.id`, `trace.events` (possibly empty), each
  event's `id`, `number`, `timestamp`, `type` and `action`, and `truncated`.
  Every other field is optional, so show a placeholder when it is missing.
- Skip event types, finding fields and enum values you do not recognise.
  Additive changes do not bump the API version.
- **Treat every string as untrusted.** Trace content is what an agent and the
  tools it ran produced. Insert it with `textContent` or
  `document.createTextNode`, never with `innerHTML`, `outerHTML`,
  `insertAdjacentHTML` or `document.write`, and never as a URL.
- Expect thousands of events. Build the DOM once, and paginate or virtualise
  long lists.

## Versioning

- `lenses/v1` is this document; `beacon.lens.v1` is the data contract.
- Adding an optional field to `LensDataV1`, the trace bundle, or the manifest
  is backward compatible and keeps both versions.
- Removing or renaming a field, changing a field's meaning, or loosening the
  sandbox is a breaking change. It ships as `beacon.lens.v2` alongside `v1`,
  and the dashboard keeps serving `v1` lenses the `v1` shape.

### Changelog

- `lenses/v1`: initial specification.
- `lenses/v1` (additive): `token_usage`, the counted usage of the trace.
