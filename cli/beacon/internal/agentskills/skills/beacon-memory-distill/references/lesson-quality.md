# Writing a Beacon memory lesson

A memory is read by a future agent that has never seen the original session, often in a
different harness, at the start of an unrelated task. Write for that reader.

## Shape

| Field | Rule |
|-------|------|
| `--title` | Imperative, specific, under 80 characters. It is the search hit and the skill description, so name the tool, file, or error. |
| `--kind` | One of the five kinds below. |
| `--applicability` | Starts with "when", "before", or "after". Names the trigger a future agent would recognise, such as a file path, a command, or an error string. |
| body | 3–10 lines. First, what to do. Then why, in one sentence, from the trace. Then how to confirm it worked. Use exact commands and paths in backticks. |
| `--tag` | 2–4 lowercase tags: the subsystem, the tool, the harness only if it matters. |
| `--reason` | Who reviewed it and which trace events support it. |

## Kinds

- `workflow`: a procedure with a required order. "Build the hooks binary before
  `go test ./...` in cli/beacon."
- `correction`: the user corrected the agent, and the correction generalises. "Use
  `testenv.SetHome`, not `t.Setenv(\"HOME\")`: Windows ignores HOME."
- `debugging_pattern`: a symptom that points to a cause that is not obvious. "`pattern
  hooks.bin: no matching files` means the embedded hooks binary was never built."
- `gotcha`: a trap with no error message, or a misleading one. "Vector 0.57+ stops
  expanding `${VAR}` in the generated packs."
- `convention`: a project rule the trace shows, not a personal preference. "Every new
  collection path sets `harness.collection_method`."

## Good

```text
Title: Build the embedded hooks binary before running cli/beacon tests
Kind: workflow
Applicability: when running go test in cli/beacon on a fresh checkout
Tags: build, testing, cli

Run `make build-hooks-current` in cli/beacon before `go test ./...`.
The package embeds hooks.bin, and on a fresh checkout the build fails with
"pattern hooks.bin: no matching files found" (trace events 14-19).
Confirm with `go build ./...`, which should exit 0 before you run the tests.
```

## Not a memory

- A summary of what happened ("The agent fixed the flaky test"). Say what to do next time.
- A one-off fact ("PR 612 was merged on Tuesday").
- Anything true only on one machine, branch, or day.
- Speculation the trace does not show. If you cannot point to the events, do not write it.
- A restatement of the repository's own docs. Memory is for what the docs do not say.

## Before you present a draft

- Every claim traces to an event number you read.
- No secret, token, credential, hostname, customer name, or personal detail appears
  anywhere in the fields.
- It would still help an agent that has never seen this session.
- It does not duplicate an existing memory (check with `beacon memory list -q`).
