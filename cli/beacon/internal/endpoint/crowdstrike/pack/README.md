# Beacon Endpoint Agent CrowdStrike AIDR Pack

This pack forwards Beacon endpoint JSONL events into CrowdStrike AIDR with a
customer-managed OpenTelemetry Collector. Beacon still writes one local source of
truth: `runtime.jsonl`. CrowdStrike AIDR URLs and collector tokens stay in the
OTel Collector environment or your deployment tooling, not in Beacon endpoint
configuration.

## What This Integration Supports

This is a CrowdStrike AIDR integration for AI-agent telemetry. It sends Beacon
events to AIDR's OpenTelemetry logs endpoint using AIDR's documented `gen_ai.*`
mapping, so events can appear in AIDR Findings, AIDR Visibility, and Falcon
Next-Gen SIEM as `AIDRPromptDataEvent` records when your account includes
Next-Gen SIEM.

It is not a Falcon sensor or CrowdStrike EDR integration. Direct Falcon
Next-Gen SIEM HEC ingestion is a separate generic log path and requires
CrowdStrike-side parser/query/dashboard work for a polished integration.

## CrowdStrike Prerequisites

- A CrowdStrike Falcon customer account in US-1, US-2, or EU-1.
- AIDR for Agents enabled for the account.
- Your Falcon user assigned an AIDR role such as `AIDR Admin`.
- An AIDR OpenTelemetry collector registered in the Falcon console.
- Docker or another way to run `otel/opentelemetry-collector-contrib`.

If you are using a trial, confirm that the trial includes AIDR for Agents. Some
public Falcon trials focus on other Falcon products, and AIDR access may require
requesting the product trial from the Falcon console or CrowdStrike sales.

## Register The AIDR Collector

1. Open Falcon and go to `AI detection and response > Visibility`.
2. Confirm the AIDR console loads. If it does not, request AIDR for Agents access
   or the `AIDR Admin` role.
3. Go to `AI detection and response > Collectors`.
4. Click `+ Collector`.
5. Select the OpenTelemetry collector under the Logging collector category.
6. For first validation, select `No Policy, Log Only`, or use a report-only
   logging policy if one is available.
7. Save the collector and open its details page.
8. From the `Config` tab, copy the AIDR base URL and token.

Export the values before starting the collector:

```bash
export CS_AIDR_BASE_URL="https://api.crowdstrike.com/aidr/aiguard"
export CS_AIDR_TOKEN="pts_..."
```

## Install

Generate this pack:

```bash
beacon endpoint crowdstrike install-pack --system --output ./beacon-crowdstrike-pack
```

The generated `otel-collector-config.yaml` points at the Beacon log path selected
by the CLI:

- User mode: `~/.beacon/endpoint/logs/runtime.jsonl`
- System mode: `/var/log/beacon-agent/runtime.jsonl`
- Custom mode: the value passed with `--log-path`

For MDM or managed endpoint deployment, prefer Beacon system mode so the OTel
Collector can tail `/var/log/beacon-agent/runtime.jsonl` without per-user home
directory ACLs.

## Run A Local Trial Collector

From the directory that contains `beacon-crowdstrike-pack`:

```bash
docker run --rm \
  -v "$(pwd)/beacon-crowdstrike-pack/otel-collector-config.yaml:/etc/otelcol/config.yaml:ro" \
  -v "/var/log/beacon-agent:/var/log/beacon-agent:ro" \
  -e CS_AIDR_BASE_URL="$CS_AIDR_BASE_URL" \
  -e CS_AIDR_TOKEN="$CS_AIDR_TOKEN" \
  otel/opentelemetry-collector-contrib:latest \
  --config /etc/otelcol/config.yaml
```

Or use Docker Compose:

```bash
cd beacon-crowdstrike-pack
export BEACON_LOG_DIR="/var/log/beacon-agent"
docker compose up
```

For a user-mode Beacon install, mount the parent directory of your user-mode log
and generate the pack without `--system`.

## Validate

Write a fresh Beacon validation event:

```bash
sudo /opt/beacon/bin/beacon endpoint crowdstrike validate --system
```

In AIDR, open `Findings` and search for:

```text
Beacon endpoint CrowdStrike AIDR validation event
```

Open `Visibility` to confirm Beacon activity appears in AIDR's AI data flows.

If your Falcon account includes Next-Gen SIEM, search:

```text
event_type="AIDRPromptDataEvent"
```

## Production Forwarding

For production, run the OTel Collector through your endpoint-management system
or another customer-managed deployment mechanism. The collector should:

- run with a secret-injected `CS_AIDR_TOKEN`,
- tail the Beacon runtime log from the system path when possible,
- preserve checkpoint state so it does not replay the entire log on restart,
- send uncompressed OTLP HTTP JSON payloads because AIDR does not support
  compressed payloads on this endpoint,
- keep Beacon as the local JSONL source of truth for investigation and support.

## Content Retention

Beacon content retention defaults to `full`, so prompt text, tool input, command
output, raw tool payloads, and other retained content may be forwarded to AIDR.
Use Beacon's `metadata` or `redacted` content retention modes for stricter
deployments.

`metadata` mode may reduce AIDR detection value because AIDR's prompt and PII
detections need content to inspect.

## Troubleshooting

- If the collector exits on startup, run it with the generated config and inspect
  the error. Common issues are a missing `CS_AIDR_TOKEN`, an unreadable Beacon
  log path, or an older collector image without the required processors.
- If no events appear in AIDR, confirm Beacon is writing new lines to
  `runtime.jsonl`, then write a validation event.
- If AIDR shows events but Next-Gen SIEM does not, confirm your CrowdStrike
  account includes Next-Gen SIEM and search for `event_type="AIDRPromptDataEvent"`.
