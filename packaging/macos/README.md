# macOS Deployment

This directory contains macOS deployment assets for the Beacon Endpoint Agent,
including Jamf- and Fleet-ready package scripts, policy helpers, inventory
queries, and Jamf Extension Attributes.

Beacon's MDM support is deployment-native: Jamf or Fleet installs and
inventories a local-only endpoint agent, while Beacon continues to write local
JSONL telemetry without requiring a hosted account, remote policy fetch, or MDM
API credentials.

## Package Layout

The package builder assembles this payload:

```text
/opt/beacon/bin/beacon
/opt/beacon/bin/beacon-otelcol
/opt/beacon/scripts/install-endpoint.sh
/opt/beacon/scripts/uninstall-endpoint.sh
/opt/beacon/jamf/extension-attributes/*.sh
/opt/beacon/jamf/scripts/*.sh
/opt/beacon/fleet/queries/*.sql
/opt/beacon/fleet/scripts/*.sh
```

The endpoint install creates system configuration and runtime state:

```text
/Library/Application Support/Beacon/Endpoint/config.json
/Library/Application Support/Beacon/Endpoint/otelcol.yaml
/Library/LaunchDaemons/com.beacon.endpoint.collector.plist
/var/log/beacon-agent/runtime.jsonl
```

## Build A Test Package

Build Beacon and `beacon-otelcol`, then assemble the macOS package:

```bash
cd cli/beacon
make build
cd ../..

cd collector-builder
ocb --config builder.yaml
cd ..

sh packaging/macos/build-pkg.sh
```

Set `PKG_SIGN_IDENTITY` to sign with `pkgbuild`, and set
`NOTARYTOOL_PROFILE` to submit and staple the package with Apple's notary
service.

## Manual Install

```bash
sudo beacon endpoint install --system
beacon endpoint status
beacon endpoint wazuh print-config
```

The macOS package installs Beacon's custom `beacon-otelcol` collector at
`/opt/beacon/bin/beacon-otelcol`, so the CLI discovers it automatically.

## Smoke Test

Run the non-root endpoint smoke test on a macOS host or VM:

```bash
sh packaging/macos/smoke-endpoint.sh
```

The smoke test builds a temporary Beacon binary, uses a temporary `HOME`, runs a
user-mode install with `--no-start`, validates status/Wazuh output, installs
Cursor hooks, and uninstalls while preserving the runtime log for assertions.

## MDM Deployment Model

Use the signed and notarized `.pkg` as the base deployment artifact. The package
installs Beacon under `/opt/beacon`, writes system endpoint configuration, and
loads the local collector LaunchDaemon. The shared endpoint wrapper installs
system-level configuration and writes logs to
`/var/log/beacon-agent/runtime.jsonl`.

For Sumo Logic, keep Beacon as the local JSONL producer and deploy a
customer-managed shipper that tails `/var/log/beacon-agent/runtime.jsonl` into a
Hosted Collector HTTP Logs & Metrics Source. Generate Beacon's Sumo content pack
for setup guidance, a one-shot smoke test, and sample events:

```bash
/opt/beacon/bin/beacon endpoint sumo install-pack --system --output ./beacon-sumo-pack
/opt/beacon/bin/beacon endpoint sumo validate --system
```

Recommended Sumo metadata is `_sourceCategory=security/agentbeacon`, source
name `agentbeacon`, and fields
`product=agentbeacon,telemetry=ai_agent,env=prod`. For production forwarding,
use a tailing shipper that checkpoints offsets, batches JSONL records, sends
gzip-compressed POST payloads, and avoids repeatedly uploading the whole file.

For Rapid7 InsightIDR, use the same local JSONL producer model and deploy a
customer-managed shipper that tails `/var/log/beacon-agent/runtime.jsonl` into a
Custom Logs webhook as NDJSON. Generate Beacon's Rapid7 content pack for setup
guidance, a one-shot smoke test, and sample events:

```bash
/opt/beacon/bin/beacon endpoint rapid7 install-pack --system --output ./beacon-rapid7-pack
/opt/beacon/bin/beacon endpoint rapid7 validate --system
```

Keep the Rapid7 webhook URL in deployment tooling or the forwarder environment,
not in Beacon endpoint configuration. For production forwarding, use a tailing
shipper that checkpoints offsets, batches JSONL records, and avoids repeatedly
uploading the whole file.

For AWS CloudWatch Logs, keep Beacon as the local JSONL producer and deploy a
customer-managed Vector host agent that tails
`/var/log/beacon-agent/runtime.jsonl` into a CloudWatch Logs log group. Generate
Beacon's AWS CloudWatch Logs content pack for setup guidance, a Vector config,
sample events, and validation search guidance:

```bash
/opt/beacon/bin/beacon endpoint cloudwatch install-pack --system --output ./beacon-cloudwatch-pack
/opt/beacon/bin/beacon endpoint cloudwatch validate --system
```

Provide `BEACON_CLOUDWATCH_LOG_GROUP`, optional
`BEACON_CLOUDWATCH_LOG_STREAM_PREFIX`, `AWS_REGION`, and AWS
credential-provider settings through the Vector service environment, host
identity, MDM, or secret tooling. Keep AWS credentials, IAM roles, log group
retention, and encryption outside Beacon endpoint configuration.

Environment variables take precedence, followed by MDM script parameters:

```text
BEACON_ENDPOINT_HARNESSES: default claude,codex
BEACON_OTLP_GRPC_PORT: default 4317
BEACON_OTLP_HTTP_PORT: default 4318
BEACON_COLLECTOR: default /opt/beacon/bin/beacon-otelcol when present
BEACON_NO_START: accepts 1/true/yes
BEACON_SPLUNK_HEC_ENDPOINT: optional Splunk HEC URL
BEACON_SPLUNK_HEC_TOKEN: optional Splunk HEC token
BEACON_SPLUNK_INDEX: optional Splunk index
BEACON_SPLUNK_SOURCE: optional Splunk source
BEACON_SPLUNK_SOURCETYPE: optional Splunk sourcetype
BEACON_SPLUNK_INSECURE_SKIP_VERIFY: accepts 1/true/yes
BEACON_SPLUNK_CA_FILE: optional CA certificate path
```

Gemini CLI is available as an opt-in local OpenTelemetry harness. Include it in
the harness list, for example `BEACON_ENDPOINT_HARNESSES=claude,codex,gemini`,
when you want the deployment to manage Gemini telemetry settings.

GitHub Copilot CLI is opt-in and MDM-managed. Do not add it to the default
Beacon harness list; instead, deploy Copilot's OTel environment in the Copilot
CLI launch environment so it exports to Beacon's local OTLP HTTP receiver:

```sh
export COPILOT_OTEL_ENABLED="true"
export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318"
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT="true"
```

`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` is optional. Beacon
validates Copilot CLI through endpoint discovery/status and normalizes events
when the MDM-managed environment sends OTLP. Copilot CLI currently exports OTLP
over HTTP, not gRPC.

Recommended rollout:

1. Upload the signed/notarized package to a pilot group.
2. Confirm the LaunchDaemon is running and `beacon endpoint wazuh validate`
   writes a validation event.
3. Add inventory signals for version, service health, log freshness, harnesses,
   and log writability.
4. Scope repair/remediation to unhealthy devices.
5. Broaden deployment in stages after inventory and validation stay healthy.

Cursor, Devin CLI, Devin Desktop, Factory, Grok Build, and opencode hook
installation is separate from the base system package because these integrations
write per-user or per-project runtime settings. Run hook helpers only when an
interactive console user is present. Cursor hooks use `.cursor/hooks.json`;
Devin CLI and Devin Desktop hooks use `.devin/hooks.v1.json` for project
installs or `~/.config/devin/config.json` for user installs; Factory hooks use
`.factory/settings.json`; Grok Build uses `.grok/hooks/beacon-endpoint.json` or
`~/.grok/hooks/beacon-endpoint.json`; opencode uses Beacon's owned plugin at
`~/.config/opencode/plugins/beacon.ts`. Restart the runtime after installation
so new sessions pick up the settings. Install hooks with the same endpoint log
path as the collector when you want hook telemetry and OTLP telemetry to appear
in one dashboard.

Factory Droid OTLP metrics are also managed outside the base system package.
Droid reads OTLP settings from its launch environment, so deploy the environment
from a user-context MDM policy or another customer-owned launch policy:

```sh
export OTEL_TELEMETRY_ENDPOINT="http://127.0.0.1:4318"
```

Beacon's endpoint status and discovery commands can validate whether the
effective Droid environment points at the local OTLP HTTP receiver. If
`OTEL_TELEMETRY_HEADERS` is needed, treat it as customer-managed secret material
and avoid storing it in Beacon defaults or package parameters.

For richer Factory prompt/session/tool/file telemetry, install Beacon's Factory
hooks in the logged-in user's context:

```bash
beacon endpoint hooks install --harness factory --level user --log-path /var/log/beacon-agent/runtime.jsonl
```

For opencode prompt/session/tool telemetry, install Beacon's opencode plugin in
the logged-in user's context:

```bash
beacon endpoint hooks install --harness opencode --level user --log-path /var/log/beacon-agent/runtime.jsonl
```

For Grok Build session/prompt/tool telemetry, install Beacon's Grok hook file.
User-level hooks are always trusted by Grok; project-level hooks require
`/hooks-trust` in Grok before they execute:

```bash
beacon endpoint hooks install --harness grok --level user --log-path /var/log/beacon-agent/runtime.jsonl
beacon endpoint hooks install --harness grok --level project --log-path /var/log/beacon-agent/runtime.jsonl
```

For Devin prompt/session/tool/approval/file telemetry, install Beacon's Devin
CLI or Devin Desktop hooks in the logged-in user's context or project context.
`devin` remains a legacy alias for `devin-cli`:

```bash
beacon endpoint hooks install --harness devin-cli --level user --log-path /var/log/beacon-agent/runtime.jsonl
beacon endpoint hooks install --harness devin-cli --level project --log-path /var/log/beacon-agent/runtime.jsonl
beacon endpoint hooks install --harness devin-desktop --level user --log-path /var/log/beacon-agent/runtime.jsonl
```

The main install path can also configure hook-backed Devin targets alongside
OTLP-backed harnesses, for example
`beacon endpoint install --harness claude,codex,devin-cli,devin-desktop`.
After installing Devin Desktop hooks, generate a Desktop event and check the
runtime log to confirm the Desktop app executed the hook file.

The opencode plugin is a small Beacon-owned TypeScript adapter that calls the
Beacon Go hook binary with raw opencode payloads. It does not depend on the
third-party `@devtheops/opencode-plugin-otel` package or configure a separate
OTLP exporter. For troubleshooting, set `BEACON_OPENCODE_DEBUG=1` in the
environment that launches opencode to enable best-effort plugin debug logs.

## Jamf Pro

Upload the generated `.pkg` to Jamf Pro and create a Policy scoped to a pilot
Smart Group. The package postinstall performs the default system install, so no
script is required for the common path.

Use `/opt/beacon/jamf/scripts/install.sh` when a policy needs explicit
parameters or a reinstall action:

```bash
/opt/beacon/jamf/scripts/install.sh "$@"
```

Jamf script parameters:

```text
Parameter 4: harnesses, default claude,codex
Parameter 5: OTLP gRPC port, default 4317
Parameter 6: OTLP HTTP port, default 4318
Parameter 7: collector path, default /opt/beacon/bin/beacon-otelcol
Parameter 8: no-start flag for install.sh only
Parameter 9: Splunk HEC endpoint for install.sh only
Parameter 10: Splunk HEC token for install.sh only
Parameter 11: Splunk index for install.sh only
Parameter 12: Splunk source for install.sh only
Parameter 13: Splunk sourcetype for install.sh only
Parameter 14: Splunk insecure TLS skip verify for install.sh only
Parameter 15: Splunk CA file for install.sh only
```

To opt in Gemini CLI telemetry through Jamf, include `gemini` in parameter 4,
for example `claude,codex,gemini`.

For GitHub Copilot CLI, keep parameter 4 at the normal default unless you want
install or repair output to call out that Copilot is MDM-managed. Configure the
Copilot launch environment separately:

```sh
export COPILOT_OTEL_ENABLED="true"
export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318"
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT="true"
```

For repair policies, prefer the `BEACON_SPLUNK_*` environment variables so
tokens do not need to be entered as visible script parameters.

Use `/opt/beacon/jamf/scripts/repair.sh` as a remediation policy for Macs where
Extension Attributes report a stale or unhealthy install. Use
`/opt/beacon/jamf/scripts/install-cursor-hooks.sh` as a separate user-context
policy for hook telemetry. Set
`BEACON_HOOK_HARNESSES=cursor,devin-cli,devin-desktop,factory,opencode` to install supported hook
integrations; the helper writes hook events to
`/var/log/beacon-agent/runtime.jsonl` by default.

Use `/opt/beacon/jamf/scripts/repair-falcon-claude-hooks.sh` when a Jamf policy
needs to repair the system endpoint, configure Falcon LogScale HEC forwarding,
prepare `/var/log/beacon-agent/runtime.jsonl` for user-run hooks, reinstall
Claude Code hooks for the interactive console user, and run a manual Claude hook
smoke test in one step.

`repair-falcon-claude-hooks.sh` Jamf script parameters:

```text
Parameter 4: Falcon LogScale HEC endpoint
Parameter 5: Falcon LogScale HEC token
Parameter 6: Falcon source, default beacon-endpoint-agent
Parameter 7: Falcon sourcetype, default json
Parameter 8: OTLP gRPC port, default 5317
Parameter 9: OTLP HTTP port, default 5318
Parameter 10: Falcon repository/index
```

### Jamf Extension Attributes

Upload scripts from `packaging/macos/jamf/extension-attributes` to inventory:

- Beacon version
- Collector service health
- Last runtime event age in seconds
- Configured harnesses
- Runtime log writability
- Splunk HEC forwarding state

Suggested Smart Groups:

- Beacon version is `not_installed`
- Collector service health is not `running`
- Last runtime event age is greater than `86400`
- Runtime log writability is not `writable` or `creatable`

### Jamf Validation

After deploying the package, run:

```bash
sudo /opt/beacon/bin/beacon endpoint status --json
sudo /opt/beacon/bin/beacon endpoint wazuh validate
sudo launchctl print system/com.beacon.endpoint.collector
```

## Fleet

Upload the signed/notarized `.pkg` as Fleet software and scope it to a pilot
team or label. The package postinstall performs the default system install.

Fleet scripts are installed under `/opt/beacon/fleet/scripts`:

```text
install.sh: reinstall or install with optional arguments
validate.sh: status JSON, Wazuh validation, and LaunchDaemon check
repair.sh: preserve logs/config while repairing collector and harness config
uninstall.sh: remove endpoint service files
```

Fleet install script positional arguments:

```text
install.sh argument 1: harnesses, default claude,codex
install.sh argument 2: OTLP gRPC port, default 4317
install.sh argument 3: OTLP HTTP port, default 4318
install.sh argument 4: collector path, default /opt/beacon/bin/beacon-otelcol
install.sh argument 5: no-start flag, accepts 1/true/yes
install.sh argument 6: Splunk HEC endpoint
install.sh argument 7: Splunk HEC token
install.sh argument 8: Splunk index
install.sh argument 9: Splunk source
install.sh argument 10: Splunk sourcetype
install.sh argument 11: Splunk insecure TLS skip verify
install.sh argument 12: Splunk CA file
```

Fleet repair script positional arguments:

```text
repair.sh argument 1: harnesses, default claude,codex
repair.sh argument 2: OTLP gRPC port, default 4317
repair.sh argument 3: OTLP HTTP port, default 4318
repair.sh argument 4: Splunk HEC endpoint
repair.sh argument 5: Splunk HEC token
repair.sh argument 6: Splunk index
repair.sh argument 7: Splunk source
repair.sh argument 8: Splunk sourcetype
repair.sh argument 9: Splunk insecure TLS skip verify
repair.sh argument 10: Splunk CA file
```

To opt in Gemini CLI telemetry through Fleet, include `gemini` in the harness
argument, for example `claude,codex,gemini`.

For GitHub Copilot CLI, deploy the same `COPILOT_OTEL_ENABLED=true` and
`OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318` environment outside the
Beacon package. Copilot is not part of the default Fleet harness argument and
Beacon does not write Copilot shell profiles or `~/.copilot/config.json`.

Add queries from `packaging/macos/fleet/queries` as Fleet policies or labels.
They cover package/service/log/config presence and freshness; run
`/opt/beacon/fleet/scripts/validate.sh` for full CLI-level validation of status,
harness configuration, Wazuh validation, and launchd health.

- `beacon-version.sql`
- `collector-service-health.sql`
- `last-event-age-seconds.sql`
- `configured-harnesses.sql`
- `runtime-log-writable.sql`
- `splunk-hec-forwarding.sql`

Recommended Fleet policies:

- Beacon install state is not `not_installed`
- Collector service health is `running`
- Last runtime event age is less than `86400`
- Endpoint config state is `present`
- Runtime log state is `present`

## Uninstall And Rollback

Use the vendor uninstall helper to remove endpoint service files. Set
`BEACON_KEEP_LOGS=1` or the first uninstall argument to preserve runtime logs
during removal. Set `BEACON_KEEP_CONFIG=1` or the second uninstall argument to
preserve harness telemetry configuration.

```bash
/opt/beacon/jamf/scripts/uninstall.sh "$@"
/opt/beacon/fleet/scripts/uninstall.sh "$@"
```

The endpoint uninstall removes service/configuration state. Package payload
removal remains under the MDM/package receipt lifecycle.

## Troubleshooting

- `launchctl print system/com.beacon.endpoint.collector` fails: run the repair
  helper and check `/Library/LaunchDaemons/com.beacon.endpoint.collector.plist`.
- `endpoint wazuh validate` fails: confirm `/var/log/beacon-agent` exists and is
  writable by the collector.
- No recent runtime events: confirm supported harnesses are configured and the
  local OTLP ports are not in use by another process.
- Cursor, Devin CLI/Desktop, Factory, or opencode hooks are missing: run the hook helper
  while a non-root console user is logged in, and confirm the helper uses the
  same runtime log path as the endpoint collector.

