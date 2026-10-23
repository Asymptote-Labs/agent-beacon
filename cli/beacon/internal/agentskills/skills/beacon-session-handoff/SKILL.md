---
name: beacon-session-handoff
description: Continue the user's current or selected local agent session in another supported runtime using Beacon. Use when the user asks to hand off, transfer, continue, or reopen work in Claude Code, Codex, Copilot CLI, Cursor, or another Beacon-supported harness.
license: MIT
compatibility: Requires the Beacon CLI.
metadata:
  author: asymptote-labs
  version: "0.1.0"
---

# Beacon session handoff

Use Beacon to transfer local session context without copying the conversation into a command line.
The destination starts independently in a new terminal or a resumable tmux session.

## Workflow

1. If the user did not provide a session ID, inspect sessions with:

   ```bash
   beacon handoff list --here
   ```

   Keep the default directory scope unless the user asks to search elsewhere. If more than one
   plausible session is returned, ask the user which one to continue.
2. Resolve the destination from the user's request. Do not invent a destination runtime.
3. After identifying an unambiguous session and destination, launch it with:

   ```bash
   beacon handoff resume <session-id> --agent <runtime> --launch auto --yes
   ```

   `--launch auto` opens another terminal in the calling cmux workspace or a new terminal on macOS,
   Windows, or a Linux desktop. In SSH and headless Linux environments it creates a detached tmux
   session and prints the attach command. If automatic launch cannot find a safe terminal surface,
   report the error and use:

   ```bash
   beacon handoff resume <session-id> --agent <runtime> --print
   ```
4. Report the returned terminal launcher or tmux attach command. Do not claim success when the
   command returns an error.

Do not run the destination agent directly inside the current harness's non-TTY command tool. Do
not add flags or environment variables that disable the destination runtime's approval prompts.
