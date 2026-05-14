# macOS Package Build Notes

Production releases should ship a signed and notarized macOS package that
installs:

- `beacon`
- `beacon-otelcol`
- Jamf policy scripts and Extension Attributes
- Wazuh content pack files for admins that want to import examples/rules

`beacon-hooks` is embedded in the `beacon` binary for Cursor hook installation.
Do not configure Cursor hooks during the base package install; use the Jamf
`install-cursor-hooks.sh` helper so the command runs in the target user's
context.

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
PKG_SIGN_IDENTITY="Developer ID Installer: Example Corp (TEAMID)" \
  sh packaging/macos/build-pkg.sh
```

Create, submit, and staple a signed/notarized package:

```bash
PKG_SIGN_IDENTITY="Developer ID Installer: Example Corp (TEAMID)" \
NOTARYTOOL_PROFILE="beacon-notary-profile" \
  sh packaging/macos/build-pkg.sh
```

The package installs files under `/opt/beacon` and runs
`/opt/beacon/scripts/install-endpoint.sh` in `postinstall` with explicit
collector and retention settings.

## Jamf Deployment

Upload the generated `.pkg` to Jamf Pro and create a Policy scoped to a pilot
Smart Group. For default deployment, no script parameters are required.

Use these optional Jamf policy parameters when calling
`/opt/beacon/jamf/scripts/install.sh` or `repair.sh`:

```text
Parameter 4: harnesses, default claude,codex
Parameter 5: content retention, default full
Parameter 6: OTLP gRPC port, default 4317
Parameter 7: OTLP HTTP port, default 4318
Parameter 8: collector path, default /opt/beacon/bin/beacon-otelcol
Parameter 9: no-start flag for install.sh only
```

Upload Extension Attributes from
`packaging/macos/jamf/extension-attributes` and build Smart Groups for missing,
unhealthy, stale, or misconfigured endpoints. Scope `repair.sh` to those Smart
Groups for automated remediation.

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

