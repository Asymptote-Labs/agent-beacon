# opencode payload fixtures

These sanitized payloads follow the OpenCode 1.18.3 runtime schemas captured on
2026-07-18. They intentionally preserve runtime field names even where the
generated `@opencode-ai/plugin` SDK types differ.

Covered payloads include:

- `chat.message`
- `tool.execute.after` for a Bash command
- terminal error `message.part.updated`
- completed assistant `message.updated` with usage and runtime cost
- structured multi-file-capable `session.diff`
- rejected `permission.replied`

Keep secrets and real prompt content out of fixtures. Use sanitized payloads that
preserve the field shape needed by `beacon-hooks --platform opencode
opencode-event`.
