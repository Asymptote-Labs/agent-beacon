# Beacon Endpoint Agent Datadog Pack

This pack forwards Beacon endpoint JSONL events into Datadog with the Datadog
Agent's native custom log collection. Beacon still writes one local source of
truth: `runtime.jsonl`. Datadog API keys and site configuration stay in the
Datadog Agent, not in Beacon endpoint configuration.

## Prerequisites

- Datadog Agent installed on the endpoint.
- Log collection enabled in `/opt/datadog-agent/etc/datadog.yaml`:

```yaml
logs_enabled: true
```

## Install

Generate this pack:

```bash
beacon endpoint datadog install-pack --output ./beacon-datadog-pack
```

Install the generated config on macOS:

```bash
sudo mkdir -p /opt/datadog-agent/etc/conf.d/beacon.d
sudo cp ./beacon-datadog-pack/conf.yaml /opt/datadog-agent/etc/conf.d/beacon.d/conf.yaml
sudo chmod 0644 /opt/datadog-agent/etc/conf.d/beacon.d/conf.yaml
sudo launchctl kickstart -k system/com.datadoghq.agent
```

The generated `conf.yaml` points at the Beacon log path selected by the CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

For MDM or managed endpoint deployment, prefer Beacon system mode so Datadog can
tail `/var/log/beacon-agent/runtime.jsonl` without per-user home directory ACLs.

## User-Mode macOS Permissions

The Datadog Agent usually runs as `_dd-agent`. When tailing a user-mode Beacon
log inside a home directory, `_dd-agent` must be able to traverse the parent
directories and read the log file. If `datadog-agent status` reports
`permission denied`, either use Beacon system mode or grant a narrow ACL for the
configured path.

## Validate

Write a fresh Beacon validation event:

```bash
beacon endpoint datadog validate
```

Check the Datadog Agent status:

```bash
sudo datadog-agent status
```

The Logs Agent section should show logs processed and sent. In Datadog Log
Explorer, search for:

```text
service:beacon-endpoint-agent vendor:beacon product:endpoint-agent
```

You can also search for the validation event without requiring a custom Datadog
facet or parsing pipeline:

```text
service:beacon-endpoint-agent "Beacon endpoint datadog validation event"
```

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, and other retained content may be forwarded to Datadog. Use Beacon's
`metadata` or `redacted` content retention modes for stricter deployments.

## OpenTelemetry Note

Datadog's DDOT Collector is a good fit for OTel-first Linux or Kubernetes
deployments, but Beacon's macOS endpoint v0 uses native Datadog Agent file log
collection because it is the supported host path for tailing local JSONL files.
