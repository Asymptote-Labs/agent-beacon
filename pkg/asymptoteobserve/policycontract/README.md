# Beacon policy provider contract

This package defines the stable, versioned boundary between a Beacon hook and an
external **policy provider** — an executable that decides whether to allow or deny
a tool call an agent runtime is about to make.

It is the public seam that lets anyone plug enforcement into Beacon without Beacon
shipping any enforcement of its own. The open Beacon build only ever **asks and
honors**: it is inert when no provider is configured and **fail-open** on any
error.

## How it works

1. A hook (`beacon-hooks pre-tool` / `permission-request`) reads the environment
   variable `BEACON_POLICY_PROVIDER`. If it is unset, the seam does nothing and
   the tool call proceeds as before.
2. If set, it is the path to a provider executable. The hook writes a single JSON
   [`Request`](./policycontract.go) to the provider's **stdin**.
3. The provider writes a single JSON [`Response`](./policycontract.go) to its
   **stdout** and exits.
4. If the response decision is `deny`, the hook returns the runtime's
   platform-specific deny shape and records `policy.enforcement=enforce` /
   `policy.decision=deny` telemetry. Anything else — including a timeout,
   non-zero exit, or malformed output — is treated as **allow**.

The request `event` is a Beacon endpoint `Event` describing the imminent tool
call, using the same field names as the runtime JSONL log and the open Threat
Rules schema (`spec/threat-rules/FIELDS.md`), so a provider can match it with the
same rule format `beacon scan` uses.

`schema.json` is the JSON Schema for the request and response objects.

## Request

```json
{
  "version": "1",
  "phase": "pre-tool",
  "platform": "claude",
  "event": {
    "vendor": "beacon",
    "product": "endpoint-agent",
    "schema_version": "1.0",
    "event": { "kind": "agent_runtime", "action": "command.executed", "category": "command" },
    "command": { "command": "claude --dangerously-skip-permissions -p \"...\"" },
    "session": { "id": "abc123" }
  }
}
```

## Response

```json
{ "decision": "deny", "reason": "permission-bypass spawn blocked", "rule_id": "agent-permission-bypass-spawn", "severity": "high", "mode": "enforce" }
```

## Minimal provider

Any language works; it just needs to read stdin and write a JSON object to
stdout. A trivial allow-all provider in shell:

```sh
#!/bin/sh
cat >/dev/null            # consume the request
printf '{"decision":"allow"}\n'
```

A reference provider that denies permission-bypass spawns lives in
[`refprovider`](./refprovider); build it and point `BEACON_POLICY_PROVIDER` at the
binary to see the seam in action.
