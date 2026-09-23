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
repository names, and severity mappings before enabling alerts in production.

## CEF and Syslog

Microsoft Sentinel can also collect CEF and Syslog through Azure Monitor Agent.
That path is useful for SOCs standardized on `CommonSecurityLog`, but it is not
the default Beacon recommendation because Beacon events are rich structured JSON
with prompts, tool calls, commands, files, runtime metadata, and optional raw
fields. Flattening those events into CEF loses useful context.

## Without Azure Monitor Agent: Vector and the Logs Ingestion API

On a machine outside Azure, Azure Monitor Agent needs Azure Arc first, and it
does not run on macOS at all. `vector.toml` is the alternative. The Vector that
ships with Beacon (`/opt/beacon/bin/vector`) posts each line to the Logs
Ingestion API with a Microsoft Entra app registration. It needs no Arc and no
agent extension.

1. Deploy `dcr-logs-ingestion-template.json` with your workspace and Data
   Collection Endpoint resource IDs, and note the `dcrImmutableId` output. It
   runs the same transform into the same `BeaconRuntime_CL` table.
2. Grant your app registration the Monitoring Metrics Publisher role on that DCR.
3. Run Vector with `vector.toml`, setting `BEACON_SENTINEL_DCE_ENDPOINT`,
   `BEACON_SENTINEL_DCR_IMMUTABLE_ID`, `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, and
   `AZURE_CLIENT_SECRET` in the Vector service's environment.

The host needs outbound HTTPS to `login.microsoftonline.com` and the DCE's
`*.ingest.monitor.azure.com` hostname. The Azure credentials live only in the
Vector service's environment, never in Beacon config.

## Content Handling

Beacon forwards retained prompt text, tool input, command output, raw tool
payloads, and related local telemetry to Microsoft Sentinel subject to Beacon's
secret redaction, sanitization, truncation, and event-size limits.
