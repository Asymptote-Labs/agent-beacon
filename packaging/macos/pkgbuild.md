# macOS Package Build Notes

Production releases should ship a signed and notarized macOS package that
installs:

- `beacon`
- `beacon-hooks` when hook integrations are enabled
- `beacon-otelcol`
- launchd plist templates
- Wazuh content pack files

The package should run `beacon endpoint install --no-start` in a
postinstall script, then bootstrap the launchd service once files and configs
are present.

Release gates:

- package signature verified with `pkgutil --check-signature`
- notarization accepted by Apple
- install/uninstall tested on a clean macOS runner or VM
- `sh packaging/macos/smoke-endpoint.sh` passes on a clean macOS runner or VM
- Wazuh validation event successfully written after install

