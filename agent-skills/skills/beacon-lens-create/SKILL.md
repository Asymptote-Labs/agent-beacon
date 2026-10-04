---
name: beacon-lens-create
description: Build a Beacon lens, a single HTML file that renders one agent trace as a purpose-built view (a hub-and-spoke tool map, a timeline of risky commands, a per-file review, a cost breakdown) inside the local Beacon dashboard. Reads the lens spec, inspects real trace data, writes the file, lints it, previews it against a real session, and installs it. Use when the user asks to "make a lens", "build a view of my traces", "visualize this session", or wants a custom tab next to the full trace in the Beacon dashboard.
license: MIT
compatibility: Requires the Beacon CLI (beacon) on PATH with endpoint capture installed, so there are traces to render. Reads only local state and makes no network calls.
metadata:
  author: asymptote-labs
  homepage: https://docs.beacon.sh/concepts/lenses
  version: "0.1.0"
---

# Beacon lens create

A **lens** is one HTML file that renders one trace. The Beacon dashboard shows lenses as
tabs on a session's page, next to the full event list. Each lens runs in a sandboxed frame
with no network access and receives the trace once, through `window.beacon.getTrace()`.
Everything else is plain HTML, CSS and JavaScript in that one file.

Your job is to turn what the user wants to see into a lens that works on their real
sessions, then install it.

## Step 1: check that Beacon is available

```bash
beacon version
```

If `beacon` is not found, tell the user that lenses need the Beacon CLI, point them to
https://docs.beacon.sh/get-started/overview, and stop. Do not install it yourself.

## Step 2: read the spec

```bash
beacon lenses spec
```

Read all of it before writing anything. It defines the manifest, the exact shape of the
data, the sandbox, the style tokens and the robustness rules. The parts that most often go
wrong:

- The manifest is a `<script type="application/beacon-lens+json">` element spelled exactly
  that way, with `id`, `title`, `version` and `api: "beacon.lens.v1"`.
- Nothing loads by URL: no CDN scripts, fonts, images or stylesheets. Inline everything.
- Trace content is untrusted. Build every node with `textContent`; never `innerHTML`.
- Every field except the few the spec guarantees is optional. Render a placeholder when one
  is missing.

For a complete, working lens to start from:

```bash
beacon lenses spec --example
```

## Step 3: look at real data

Do not guess the data shape from the spec alone. Print exactly what a lens would receive
for a real session:

```bash
# The most recent session
beacon lenses data

# A specific session (IDs are on the dashboard's session pages)
beacon lenses data --session <session-id>
```

The output includes whatever content the log retained (prompts, command output, diffs), so
read it locally and do not paste large amounts of it back to the user. Note which fields the
user's sessions actually carry: event `type` values, whether `file.diff` or `usage` is
present, whether there are `findings`. Design for those, and handle their absence.

## Step 4: write the lens

Pick a short, lowercase, hyphenated `id` that is not a built-in. `beacon lenses list` shows
the ones in use. Write the file in the user's project or a scratch directory, not in the
lens store. Follow the spec's layout rules:

- Render a loading line immediately, then fill it in when `getTrace()` resolves; show the
  error message if it rejects.
- Use the `--beacon-*` style tokens with fallbacks, compact tables over cards, 13px text at
  weight 400, right-aligned numbers.
- Let the page grow; never size the root to `100vh`. The dashboard sizes the frame.
- Expect thousands of events. Build the DOM once, and cap or paginate long lists.

## Step 5: lint until clean

```bash
beacon lenses lint path/to/my-lens.lens.html
```

Errors stop the lens from running. Warnings name code the sandbox will block or the spec
forbids, with a line number. Fix both; use `--strict` to make warnings fail the check:

```bash
beacon lenses lint --strict path/to/my-lens.lens.html
```

## Step 6: preview it against a real session

```bash
beacon lenses preview path/to/my-lens.lens.html --session <session-id>
```

This serves the dashboard with the lens added and prints the session page to open (pass
`--open` to open a browser). It runs until interrupted, so start it in the background if
your harness blocks on long-running commands. The file is read again on every page load:
edit, reload, repeat. Ask the user to look at it, and check at least one session that lacks
the data the lens is about (no diffs, no usage, no findings) to see the empty state.

If the lens shows "could not be shown" and the dashboard falls back to the full session,
the lens threw before rendering, never called `getTrace()`, or navigated its own frame. The
notice says which.

## Step 7: install it

When the user is happy with it:

```bash
beacon lenses add path/to/my-lens.lens.html
```

It now appears in the **+** menu on every session page. To change it later, edit the source
file, bump `version` in its manifest, and run `beacon lenses add` again. `beacon lenses list`
shows what is installed and `beacon lenses remove <id>` uninstalls it.

## Rules

- Never weaken the sandbox or ask the user to: a lens that needs the network is the wrong
  design. Everything it needs is in `getTrace()`.
- Never put trace content into a lens file. The lens renders data at view time; the file
  holds only code.
- Never install a lens the user has not seen in preview.
