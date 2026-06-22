# macOS Package Build Notes

Production releases should ship a signed and notarized macOS package that
installs:

- `beacon`
- `beacon-otelcol`
- Jamf policy scripts and Extension Attributes
- Fleet scripts and osquery policy/label examples
- Wazuh content pack files for admins that want to import examples/rules

`beacon-hooks` is embedded in the `beacon` binary for Cursor and Factory hook
installation. Do not configure hooks during the base package install; use the
Jamf `install-cursor-hooks.sh` helper or an equivalent Fleet user-context script
so the command runs in the target user's context.

## Build Inputs

Build the Beacon CLI first:

```bash
cd cli/beacon
make build
cd ../..
```

Build the custom OpenTelemetry Collector distribution with the Collector
Builder:

```bash
cd collector-builder
ocb --config builder.yaml
cd ..
```

The package builder expects:

```text
cli/beacon/beacon
collector-builder/dist/beacon-otelcol/<goos>_<goarch>/beacon-otelcol
```

Override those paths with `BEACON_BIN`, `BEACON_COLLECTOR_TARGET`, and
`BEACON_COLLECTOR` if release automation builds into a different directory.

The Homebrew release archive also includes `beacon-otelcol` from
the matching `collector-builder/dist/beacon-otelcol/<goos>_<goarch>` directory
and installs it beside `beacon`, so `beacon endpoint install` works without
extra flags.

## Package Build

Create an unsigned local test package:

```bash
sh packaging/macos/build-pkg.sh
```

Create a signed package:

```bash
BEACON_APP_SIGN_IDENTITY="Developer ID Application: Example Corp (TEAMID)" \
PKG_SIGN_IDENTITY="Developer ID Installer: Example Corp (TEAMID)" \
  sh packaging/macos/build-pkg.sh
```

Create, submit, and staple a signed/notarized package:

```bash
BEACON_APP_SIGN_IDENTITY="Developer ID Application: Example Corp (TEAMID)" \
PKG_SIGN_IDENTITY="Developer ID Installer: Example Corp (TEAMID)" \
NOTARYTOOL_PROFILE="beacon-notary-profile" \
  sh packaging/macos/build-signed-notarized-pkg.sh
```

The wrapper delegates package assembly to `build-pkg.sh`, so payload binaries
are signed before they are sealed into the installer. It then verifies the
package signature with `pkgutil`, validates the stapled notarization ticket,
runs Gatekeeper's install assessment, and rewrites the SHA-256 checksum.

The package installs files under `/opt/beacon` and runs
`/opt/beacon/scripts/install-endpoint.sh` in `postinstall` with explicit
collector settings, matching the shared MDM install wrapper.

opencode support is intentionally not part of the default package install. To
enable opencode prompt/session/tool telemetry, run Beacon's hook installer in the
target user's context:

```bash
beacon endpoint hooks install --harness opencode --level user --log-path /var/log/beacon-agent/runtime.jsonl
```

This writes Beacon's owned plugin to `~/.config/opencode/plugins/beacon.ts`. The
plugin is only an opencode adapter; Beacon's Go hook binary handles event
normalization, retention, redaction, and JSONL output. Set
`BEACON_OPENCODE_DEBUG=1` in the opencode launch environment only when
troubleshooting plugin delivery.

Grok Build support is also installed separately in the target user's context:

```bash
beacon endpoint hooks install --harness grok --level user --log-path /var/log/beacon-agent/runtime.jsonl
```

This writes Beacon's owned hook file to `~/.grok/hooks/beacon-endpoint.json`. Project
installs write `.grok/hooks/beacon-endpoint.json` and require `/hooks-trust` in Grok before
hooks execute.

## Jamf Deployment

Upload the generated `.pkg` to Jamf Pro and create a Policy scoped to a pilot
Smart Group. For default deployment, no script parameters are required.

Use these optional Jamf policy parameters when calling
`/opt/beacon/jamf/scripts/install.sh` or `repair.sh`:

```text
Parameter 4: harnesses, default claude,codex
Parameter 5: OTLP gRPC port, default 4317
Parameter 6: OTLP HTTP port, default 4318
Parameter 8: collector path, default /opt/beacon/bin/beacon-otelcol
Parameter 9: no-start flag for install.sh only
Parameter 10: Splunk HEC endpoint for install.sh only
Parameter 11: Splunk HEC token for install.sh only
Parameter 12: Splunk index for install.sh only
Parameter 13: Splunk source for install.sh only
Parameter 14: Splunk sourcetype for install.sh only
Parameter 15: Splunk insecure TLS skip verify for install.sh only
Parameter 16: Splunk CA file for install.sh only
```

Gemini CLI telemetry is opt-in. Include `gemini` in the harness parameter, for
example `claude,codex,gemini`, when the package deployment should manage Gemini
local OpenTelemetry settings.

For repair workflows, pass Splunk settings with `BEACON_SPLUNK_*` environment
variables when possible so HEC tokens are not exposed as script parameters.
Falcon LogScale HEC settings are environment-only and are not exposed as package
positional parameters:

```bash
BEACON_FALCON_HEC_ENDPOINT="https://cloud.us.humio.com/api/v1/ingest/hec" \
BEACON_FALCON_HEC_TOKEN="$LOGSCALE_INGEST_TOKEN" \
BEACON_FALCON_SOURCE="beacon-endpoint-agent" \
BEACON_FALCON_SOURCETYPE="json" \
  /opt/beacon/scripts/install-endpoint.sh
```

Use `BEACON_FALCON_INDEX` only with LogScale organization or system
multi-repository ingest tokens. Repository-scoped ingest tokens already choose
the target repository.

The packaged Jamf samples also include
`/opt/beacon/jamf/claude/common/repair-hooks.sh` for pilots that need a
single policy to repair the system endpoint, prepare the shared runtime log for
user-run hooks, reinstall Claude Code hooks for the console user, and run a
manual hook smoke test. Destination-specific forwarding policies live under
`/opt/beacon/jamf/claude/<destination>`, sourced from
[`packaging/macos/jamf/claude/<destination>`](https://github.com/Asymptote-Labs/agent-beacon/tree/main/packaging/macos/jamf/claude).

Upload Extension Attributes from
`packaging/macos/jamf/extension-attributes` and build Smart Groups for missing,
unhealthy, stale, or misconfigured endpoints. Scope `repair.sh` to those Smart
Groups for automated remediation.

## Fleet Deployment

Upload the generated `.pkg` as Fleet software and scope it to a pilot team or
label. For default deployment, no post-install script is required because the
package postinstall performs the system install.

Fleet remediation and validation scripts are packaged under
`/opt/beacon/fleet/scripts`, and osquery examples are packaged under
`/opt/beacon/fleet/queries`. Use the queries as Fleet policies or labels for
missing, unhealthy, stale, or misconfigured endpoints. Scope `repair.sh` to
hosts that fail the collector health, log freshness, retention, or log
writability policies.

Splunk HEC forwarding can be configured with:

```bash
BEACON_SPLUNK_HEC_ENDPOINT="https://splunk.example:8088/services/collector" \
BEACON_SPLUNK_HEC_TOKEN="$SPLUNK_HEC_TOKEN" \
BEACON_SPLUNK_INDEX="beacon" \
  /opt/beacon/scripts/install-endpoint.sh
```

Falcon LogScale HEC forwarding can be configured with:

```bash
BEACON_FALCON_HEC_ENDPOINT="https://cloud.us.humio.com/api/v1/ingest/hec" \
BEACON_FALCON_HEC_TOKEN="$LOGSCALE_INGEST_TOKEN" \
BEACON_FALCON_SOURCETYPE="json" \
  /opt/beacon/scripts/install-endpoint.sh
```

Release gates:

- `sh packaging/macos/test-endpoint-scripts.sh` passes
- `sh packaging/macos/smoke-endpoint.sh` passes on a clean macOS runner or VM
- package signature verified with `pkgutil --check-signature`
- notarization accepted by Apple
- install/uninstall tested on a clean macOS runner or VM
- Wazuh validation event successfully written after install
- `sudo launchctl print system/com.beacon.endpoint.collector` reports the
  collector service as loaded/running
- Jamf Extension Attributes report expected values after a recon
- Fleet queries report expected values after host detail refresh

