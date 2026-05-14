# macOS Deployment

This directory contains macOS deployment assets for the Beacon Endpoint Agent,
including Jamf-ready package scripts, policy helpers, and Extension Attributes.

Beacon's Jamf support is deployment-native: Jamf installs and inventories a
local-only endpoint agent, while Beacon continues to write local JSONL telemetry
without requiring a hosted account or Jamf Pro API credentials.

## Package Layout

The package builder assembles this payload:

```text
/opt/beacon/bin/beacon
/opt/beacon/bin/beacon-otelcol
/opt/beacon/scripts/install-endpoint.sh
/opt/beacon/scripts/uninstall-endpoint.sh
/opt/beacon/jamf/extension-attributes/*.sh
/opt/beacon/jamf/scripts/*.sh
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

## MDM Install Script

Use `install-endpoint.sh` from Jamf, Kandji, or a generic macOS MDM command
runner. The script installs system-level endpoint configuration and writes logs
to `/var/log/beacon-agent/runtime.jsonl`.

Environment variables take precedence, followed by Jamf script parameters:

```text
Parameter 4: harnesses, default claude,codex
Parameter 5: content retention, default full
Parameter 6: OTLP gRPC port, default 4317
Parameter 7: OTLP HTTP port, default 4318
Parameter 8: collector path, default /opt/beacon/bin/beacon-otelcol when present
Parameter 9: no-start flag, accepts 1/true/yes
```

Example Jamf policy script configuration:

```bash
/opt/beacon/jamf/scripts/install.sh "$@"
```

Use `packaging/macos/jamf/scripts/repair.sh` as a remediation policy for Macs
where Extension Attributes report a stale or unhealthy install.

## MDM Uninstall Script

Use `uninstall-endpoint.sh` to remove endpoint service files. Set
`BEACON_KEEP_LOGS=1` or Jamf parameter 4 to preserve runtime logs during
removal. Set `BEACON_KEEP_CONFIG=1` or Jamf parameter 5 to preserve harness
telemetry configuration.

## Jamf Extension Attributes

Upload scripts from `packaging/macos/jamf/extension-attributes` to Jamf Pro to
inventory:

- Beacon version
- Collector service health
- Last runtime event age in seconds
- Content retention mode
- Configured harnesses
- Runtime log writability

Suggested Smart Groups:

- Beacon version is `not_installed`
- Collector service health is not `running`
- Last runtime event age is greater than `86400`
- Content retention is not `full`
- Runtime log writability is not `writable` or `creatable`

## Jamf Validation

After deploying the package, run:

```bash
sudo /opt/beacon/bin/beacon endpoint status --json
sudo /opt/beacon/bin/beacon endpoint wazuh validate
sudo launchctl print system/com.beacon.endpoint.collector
```

