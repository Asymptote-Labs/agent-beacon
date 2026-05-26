# Security

Beacon is endpoint-local telemetry for supported AI agent runtimes. It collects
supported local runtime activity, normalizes that activity into Beacon endpoint
events, and writes those events to local JSONL for local review or
customer-controlled forwarding.

For the full enterprise review package, see the
[Beacon security review](https://docs.asymptotelabs.ai/cli/security-review).

## Default Posture

| Area | Default behavior |
| --- | --- |
| Collection | Local OpenTelemetry receivers on `127.0.0.1` or local `beacon-hooks` adapter execution |
| Content retention | Configurable as `metadata`, `redacted`, or `full` |
| Forwarding | Optional and customer configured |
| Hosted dependency | None required for normal endpoint collection or hook execution |
| Removal | Endpoint uninstall removes managed service and configuration files, with explicit flags to keep logs or config |
| Storage | Local `runtime.jsonl` on the endpoint, bounded by rotation at 10 MiB with five numbered archives |

## Data Flow and Threat Model

Beacon receives supported runtime signals through localhost OpenTelemetry
receivers or local hook execution, normalizes them locally, and writes one JSON
object per line to the runtime log. The local dashboard reads that log over
loopback, and any SIEM forwarding is configured and controlled by the customer.

Read the full
[data flow and threat model](https://docs.asymptotelabs.ai/cli/security-review-data-flow-threat-model).

## Data Inventory

Beacon writes normalized endpoint events only when a supported runtime provides
telemetry through a configured local surface. Required event fields identify the
event, endpoint, and harness; optional entities add session, tool, command,
file, approval, MCP-like, prompt, content, destination, and health context when
available and allowed by retention settings.

Review the full
[data inventory](https://docs.asymptotelabs.ai/cli/security-review-data-inventory).

## Retention and Redaction

Content retention controls how much prompt text, command output, raw attributes,
tool input, and diff content Beacon can write to local JSONL. Use `metadata` to
exclude retained content, `redacted` to apply local redaction and size limits, or
`full` only when full local or customer-controlled content capture is approved.

Compare the retention modes in
[retention and redaction](https://docs.asymptotelabs.ai/cli/security-review-retention-redaction).

## Endpoint Operations

Beacon endpoint operations are local to the managed machine. User-mode installs
use per-user paths, while system-mode installs use root-managed configuration,
log paths, and a local macOS LaunchDaemon. Runtime log storage is bounded: the
active log rotates at 10 MiB and Beacon keeps up to five numbered local
archives. Normal endpoint collection does not require a hosted account, remote
policy fetch, MDM API credentials, or external network connection.

See paths, permissions, network behavior, and uninstall guarantees in
[endpoint operations](https://docs.asymptotelabs.ai/cli/security-review-endpoint-operations).

## Security Policy

Report suspected vulnerabilities or security concerns to
[security@asymptotelabs.ai](mailto:security@asymptotelabs.ai). Include the
affected Beacon version, operating system and architecture, install mode,
reproduction steps, expected impact, and any coordinated disclosure constraints
when available.

For disclosure guidance and release verification details, see the
[security policy](https://docs.asymptotelabs.ai/cli/security-review-policy).

## Endpoint Event Schema

Beacon endpoint events are JSONL records with a stable schema contract for local
inspection and customer-managed ingestion pipelines. Each event includes required
context such as `timestamp`, `vendor`, `product`, `schema_version`, `event`,
`severity`, `endpoint`, and `harness`, with optional entities added when the
source provides them.

Inspect the normalized contract in the
[endpoint event schema](https://docs.asymptotelabs.ai/cli/event-schema).

## Related Resources

- [Beacon architecture](https://docs.asymptotelabs.ai/cli/architecture)
- [For Security & IT Teams](https://docs.asymptotelabs.ai/cli/security-it-teams)
