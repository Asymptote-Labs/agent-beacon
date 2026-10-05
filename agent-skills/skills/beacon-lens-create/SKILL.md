---
name: beacon-lens-create
description: Create, revise, debug or validate a Beacon lens, a single HTML file that renders one agent trace as a purpose-built view (a per-file review, a cost breakdown, a timeline of risky commands, a map of tool use) in a sandboxed tab of the local Beacon dashboard, fed once through window.beacon.getTrace(). Use when the user asks to "make a lens", "build a view of my traces", "visualize this session", wants a custom tab next to the full session in the Beacon dashboard, or when a lens fails to load, renders wrong, or needs checking before install.
license: MIT
compatibility: Requires the Beacon CLI (beacon) on PATH with endpoint capture installed, so there are traces to render. Reads only local state and makes no network calls.
metadata:
  author: asymptote-labs
  homepage: https://docs.beacon.sh/concepts/lenses
  version: "0.1.0"
---

# Beacon lens create

A **lens** is one self-contained HTML file that renders one agent trace: the prompts, agent
messages, tool calls, commands, file edits, approvals, token usage and threat-rule findings
Beacon recorded for a session. The local dashboard (`beacon endpoint dashboard`) shows lenses as
tabs on a session's page, next to **Full session**, and runs each one in a locked-down frame.

You write the file, check it against the user's real sessions, and install it. Nothing is
published anywhere: lenses live on this machine.

## Step 1: check that Beacon is available

```bash
beacon version
```

If `beacon` is not found, tell the user that lenses need the Beacon CLI, point them to
https://docs.beacon.sh/get-started/overview, and stop. Do not install it yourself.

## Step 2: decide what the lens shows

Start from the user's description. If it is missing, or leaves a choice open that changes what
the numbers mean (what counts as a "turn", a "failure", a "risky" command, which cost total), ask
one focused question before building. Define each unit from this request, not from another lens,
and label anything derived or estimated as such.

## Step 3: read the spec and the real data

```bash
beacon lenses spec              # the format, the data, the sandbox, the style tokens
beacon lenses spec --example    # a complete working lens to start from
beacon lenses list              # lens ids already in use
beacon lenses data              # exactly what getTrace() returns for the latest session
beacon lenses data --session <session-id>
```

Read the whole spec before writing anything. Then look at real data rather than guessing its
shape from the spec: note which event `type`s the user's sessions carry, whether `file.diff`,
`command.output` or `usage` is present, and whether `findings` exists. Design for those, and for
sessions that lack them. The output contains whatever content the log retained (prompts, command
output, diffs), so read it locally and do not paste large amounts back to the user.

## Step 4: write the file

One HTML file. No build step, bundler or `package.json`. Write it in the user's project or a
scratch directory, never directly in the lens store.

### Hard limits

The dashboard enforces these; a lens that breaks them fails to install or fails to run.

- **One file, at most 16 MiB**, with every script, style, image and font inline (`data:` URIs for
  images and fonts). Inlining a library is allowed but counts against the size and costs parse
  time on every open; prefer a few lines of your own.
- **A manifest**, in exactly one element whose opening tag is spelled exactly
  `<script type="application/beacon-lens+json">`, holding `id`, `title`, `version` and
  `"api": "beacon.lens.v1"` (optionally `description`, `icon`, `author`; unknown keys are an
  error). The `id` is lowercase letters, digits and hyphens, and cannot be a built-in lens's id.
- **No network and nothing loaded by URL.** The frame's Content Security Policy blocks `fetch`,
  XHR, WebSockets, `EventSource`, `sendBeacon`, remote scripts, stylesheets, fonts and images,
  `@import`, workers, nested frames, `eval` and `new Function`.
- **No dashboard access.** The frame is `sandbox="allow-scripts"` with an opaque origin: no
  cookies, `localStorage`, `sessionStorage` or IndexedDB, no access to the parent page, no popups
  or forms. Keep state in memory.
- **Never navigate the frame.** The dashboard closes a lens that loads a second document.

### The data

The host defines `window.beacon` before your scripts run. Its one call is the whole data API:

```js
const data = await window.beacon.getTrace();
```

It resolves once with everything you get and never refreshes. Calling it again returns the same
promise. It rejects with an `Error` if the trace does not arrive within 10 seconds; show that
message instead of a blank tab.

The result is `LensDataV1`; `beacon lenses spec` has the full shape. The parts you will use most:

- `trace.summary`: title, `started_at`/`ended_at`, `event_count`, `harness.name`, `session`,
  `repository`, `model`, `token_usage`.
- `trace.events[]`, in order, each with `id`, `number`, `timestamp`, `type`, `action` and
  optional `title`, `summary`, `fidelity`, `content`, `tool`, `command`, `file`, `mcp`,
  `approval`, `model`, `usage`, `tool_call_id`. `type` is one of `user_message`,
  `agent_message`, `agent_reasoning`, `tool_call`, `tool_result`, `command`, `file`, `mcp`,
  `approval`, `token_usage`, `session`, `error`, `other`, and the list can grow.
- `findings`: threat-rule matches, each pointing at its evidence by `event_ids`.
- `token_usage` and `token_coverage`: the trace's counted usage, and whether this runtime reports
  usage at all.
- `truncated`: the trace was cut to fit the size cap.

Things that are easy to get wrong:

- **Retained text is an object, not a string.** Prompts, messages, `command.output` and
  `file.diff` arrive as `{text?, included, retention, redacted?, truncated?, hash?, bytes?}`.
  When `included` is false the text was not kept or was redacted; show the `hash` and `bytes`
  instead of an empty box, and say why.
- **Missing is not empty.** An absent `findings` means the trace was not scanned; an empty
  `findings.items` means it was scanned and nothing matched. Say which. The same goes for
  `token_usage`.
- **Do not sum `usage` across events for a total.** Some runtimes report the same tokens on two
  channels or as cumulative counters. `token_usage.totals` is the deduplicated count; if you
  also show per-event figures, say they can add up to more. `cost_usd` is only ever what the
  runtime reported; Beacon never estimates cost, and neither should a lens.
- **`fidelity: "inferred"`** marks an action Beacon derived rather than observed (for example an
  approval built from a pre-tool notification). Show the difference when it matters.
- **Pair calls with results on `tool_call_id`**, not on adjacency, and order by `number` or
  `timestamp`, not array position.
- **Be honest about truncation.** When `truncated` is true, or `trace.events.length` is less than
  `trace.summary.event_count`, say "over the first N of M events" instead of presenting a partial
  total as the whole trace. A finding's evidence may point at an event that is not in the bundle.

### Rules for the code

- **Only these fields are guaranteed:** `api_version`, `trace.schema_version`, `trace.id`,
  `trace.summary.id`, `trace.events` (possibly empty), each event's `id`, `number`, `timestamp`,
  `type` and `action`, and `truncated`. Everything else can be missing. Show a placeholder, never
  `undefined` or `NaN`, and never throw on a missing field.
- **Skip what you do not recognise**: new event types, enum values and fields appear without a
  version bump.
- **Treat every string as untrusted.** Trace content is whatever the agent and its tools produced,
  often HTML, code and markup. Build nodes with `textContent` or `document.createTextNode`; never
  `innerHTML`, `outerHTML`, `insertAdjacentHTML` or `document.write`, and never use trace content
  as a URL.
- **Render immediately**: a loading line first, then the content when `getTrace()` resolves, an
  error line if it rejects, and an empty state for a trace with nothing to show. Keep the states
  mutually exclusive; if you use the `hidden` attribute, add `[hidden] { display: none !important; }`
  so your own `display` rules cannot reveal two states at once.
- **Expect thousands of events.** Build the DOM once; summarise, collapse or paginate long lists
  instead of rendering every row.
- No settings and no persistence; there is nowhere to save anything.

### Look like the dashboard

The lens renders inside a session page, under the page's own heading and tab bar, so it should
read as that tab's content rather than an embedded widget.

- **No page chrome.** Do not open with a title, `<h1>` or eyebrow text; the tab label is the
  heading. Add an `<h2>` only where a later section needs one. Leave `html` and `body`
  transparent, with no background, no outer padding or margin (`body { margin: 0 }`), and no
  outer border.
- **Full width.** Do not cap the content with a centred `max-width`.
- **Let it grow.** The dashboard sizes the frame to your document's height as it changes. Never
  size the root to `100vh` or `height: 100%`, and do not build an inner scrolling container.
- **Use the dashboard's tokens, each with a fallback** so the file also renders on its own:
  `--beacon-text`, `--beacon-muted`, `--beacon-line` (hairlines), `--beacon-panel-soft`
  (recessed fills), `--beacon-accent` and `--beacon-accent-soft` (links, selection, focus, one
  emphasis), `--beacon-danger`, `--beacon-warn`, `--beacon-ok` (only where they mean something),
  `--beacon-font-sans` and `--beacon-font-mono`. Take colours from these tokens rather than
  inventing a palette, so the lens follows the dashboard if its theme changes.
- **Type.** Body 13–14px. Monospace at about 13px for commands, paths, tool names, hashes, IDs
  and number columns. Two weights only, 400 and 500; headings are 400 and get their hierarchy
  from size. Use as few sizes, colours and spacing values as you can.
- **Dense and flat.** Compact tables and tight rows over padded cards; one hairline between rows
  rather than a box around each; no shadows or gradients; a light fill on hover. Right-align
  numbers and format them with `Intl.NumberFormat`; format times as short dates or relative times.
- **Motion.** Almost none: under 150 ms, and off under `prefers-reduced-motion: reduce`.
- Check it at a narrow and a wide width; the frame is as wide as the session page.

## Step 5: lint until clean

```bash
beacon lenses lint --strict path/to/my-lens.lens.html
```

Errors (size, a missing or invalid manifest) stop the lens from running. Warnings name the line
where the lens does something the sandbox blocks or the spec forbids: HTML parsing of content,
network calls, resources loaded by URL, browser storage, navigation, viewport-height layouts, or
never calling `getTrace()`. Fix both.

## Step 6: preview it against real sessions

```bash
beacon lenses preview path/to/my-lens.lens.html --session <session-id>
```

This serves the dashboard with the lens added and prints the session page to open (`--open`
opens a browser). It runs until interrupted, so start it in the background if your harness
blocks on long-running commands. The file is read again on every page load: edit, reload,
repeat. Nothing is installed.

Ask the user to look at it, and check at least one session that lacks the data the lens is about
(no diffs, no usage, no findings) to see the empty state. If the dashboard shows "The … lens
could not be shown" and falls back to the full session, the notice gives the reason:

- **it raised an error**: an exception before the lens settled, usually a missing field. Guard it.
- **it did not render within 10 seconds**: the lens never called `getTrace()`, or blocked the
  main thread on a large trace.
- **it navigated away from itself**: something set `location`, submitted a form or followed a
  link inside the frame.

## Step 7: install it

Only after the user has seen it in preview and is happy with it:

```bash
beacon lenses add path/to/my-lens.lens.html
```

It now appears in the **+** menu on every session page. To change it later, edit the source
file, bump `version` in its manifest, and run `beacon lenses add` again; an equal or lower
version is refused unless the user asks for `--force`. `beacon lenses remove <id>` uninstalls it.

## Rules

- Never weaken the sandbox or ask the user to. A lens that needs the network is the wrong design;
  everything it can have is in `getTrace()`.
- Never put trace content into the lens file. The file holds only code; it renders data at view
  time.
- Never install a lens the user has not seen in preview, and never use a built-in lens's id.
