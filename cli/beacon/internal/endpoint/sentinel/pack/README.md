# Beacon Endpoint Agent Microsoft Sentinel Pack

This pack forwards Beacon endpoint JSONL events into Microsoft Sentinel through
Azure Monitor Agent custom log collection. Beacon still writes one local source
of truth: `runtime.jsonl`. Azure tenant IDs, client secrets, workspace IDs, DCR
identifiers, and ingestion endpoints stay in Azure Monitor, deployment tooling,
or customer-managed forwarders, not in Beacon endpoint configuration.

## Prerequisites

- Beacon endpoint installed and writing local JSONL.
- A Log Analytics workspace with Microsoft Sentinel enabled.
- Azure Monitor Agent installed or deployed to the endpoint.
- A custom Log Analytics table named `BeaconRuntime_CL`.
- A Data Collection Rule associated with the endpoint and workspace.

For MDM or managed endpoint deployment, prefer Beacon system mode so Azure
Monitor Agent can tail `/var/log/beacon-agent/runtime.jsonl` without per-user
home directory ACLs.

## Install

Generate this pack:

```bash
beacon endpoint sentinel install-pack --output ./beacon-sentinel-pack
```

The generated `dcr-template.json` points at the Beacon log path selected by the
CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

## Sentinel Setup

1. Create the `BeaconRuntime_CL` custom table using `table-schema.json`.
2. Create or update an Azure Monitor Data Collection Rule using
   `dcr-template.json`.
3. Associate the DCR with endpoints that run Azure Monitor Agent.
4. Confirm the DCR uses the transform in `dcr-transform.kql`.
5. Wait for Azure Monitor Agent to tail new lines from `runtime.jsonl`.

The DCR uses a Custom Text Logs source because Beacon writes newline-delimited
JSON. The transform parses each `RawData` line with `todynamic(RawData)`,
projects stable columns for common hunting workflows, and preserves the original
Beacon event in `RawData`.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint sentinel validate
```

After Azure Monitor Agent ships the new line, validate in Microsoft Sentinel or
Log Analytics:

```kql
BeaconRuntime_CL
| where TimeGenerated > ago(24h)
| where Message has "Beacon endpoint Sentinel validation event"
| project TimeGenerated, HostName, UserName, HarnessName, EventAction, Message
```

If the validation query does not return data, check the DCR association, the
Azure Monitor Agent health state, the configured file path, and the table schema.
The target table must exist before the DCR can route transformed records to it.

## Hunting Content

Use `queries.kql` for validation and starter hunting queries. Use
`detections.kql` as example analytics rule logic; review and tune thresholds,
repository names, content retention policy, and severity mappings before
enabling alerts in production.

## CEF and Syslog

Microsoft Sentinel can also collect CEF and Syslog through Azure Monitor Agent.
That path is useful for SOCs standardized on `CommonSecurityLog`, but it is not
the default Beacon recommendation because Beacon events are rich structured JSON
with prompts, tool calls, commands, files, runtime metadata, and optional raw
fields. Flattening those events into CEF loses useful context.

## Direct Logs Ingestion API

The Azure Monitor Logs Ingestion API can be useful for a centralized,
customer-managed forwarder, but it should not be configured directly in Beacon
endpoint agent state. Direct API forwarding requires Microsoft Entra
credentials, DCR identifiers, ingestion endpoints, batching, retries, and
network failure handling. Keep those concerns outside Beacon's local endpoint
collector unless you are building a separate managed forwarder.

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, raw tool payloads, and other retained content may be forwarded to
Microsoft Sentinel. Use Beacon's `metadata` or `redacted` content retention
modes for stricter deployments.
