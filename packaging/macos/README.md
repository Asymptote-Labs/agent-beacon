# macOS Deployment

This directory contains lightweight deployment scaffolding for the Beacon
Endpoint Agent MVP.

The alpha install path assumes the `beacon` binary is already available on the
endpoint through Homebrew, a direct download, or an MDM-managed package.

## Manual Install

```bash
sudo beacon endpoint install
beacon endpoint status
beacon endpoint wazuh print-config
```

## MDM Install Script

Use `install-endpoint.sh` from Jamf, Kandji, or a generic macOS MDM command
runner. The script installs system-level endpoint configuration and writes logs
to `/var/log/beacon-agent/runtime.jsonl`.

Set `BEACON_ENDPOINT_HARNESSES` to override the default `claude,codex` harness
list.

## MDM Uninstall Script

Use `uninstall-endpoint.sh` to remove endpoint service files. Set
`BEACON_KEEP_LOGS=1` to preserve runtime logs during removal.

