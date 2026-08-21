# Cline payload fixtures

These payloads are **constructed from Cline's published hook documentation, not captured from a
running Cline install**. That is a deliberate limitation of this pack and the reason the mapper's
readers accept several spellings per field: Cline documents the base fields every hook receives
(`clineVersion`, `hookName`, `timestamp`, `taskId`, `workspaceRoots`, `userId`) and the hook
handlers a plugin implements (`beforeRun`, `beforeTool`, `afterTool`, `afterRun`), but not the field
names inside each handler's context object.

What the fixtures do pin is the part Beacon controls: the envelope the Beacon-managed Cline plugin
sends to `beacon-hooks --platform cline cline-event`. The mapper's job is to turn that envelope into
endpoint events, and these files are that contract.

Covered payloads:

- `task_start.json` — `beforeRun` carrying the message that started the task
- `tool_after_write.json` — `afterTool` for `write_to_file`, with a workspace-relative path
- `tool_after_command.json` — `afterTool` for `execute_command`, with output and an exit code
- `run_end_usage.json` — `afterRun` with runtime-reported token counts and cost

Replace a fixture with a captured payload whenever one becomes available, and keep the field
tolerance in the mapper until every fixture here is a real capture.

Keep secrets and real prompt content out of fixtures. Use sanitized payloads that preserve the
field shape needed by `beacon-hooks --platform cline cline-event`.
