# Beacon Endpoint Agent Wazuh Pack

Install `beacon-rules.xml` on the Wazuh manager and add the localfile
snippet to the Wazuh agent configuration on managed endpoints.

The generated `ossec-localfile.xml` should point at the endpoint runtime log
configured on the endpoint.

Validate locally with:

```bash
beacon endpoint wazuh validate
```

For local Wazuh Dashboard testing, apply Beacon-oriented Discover columns with:

```bash
WAZUH_DASHBOARD_URL=https://localhost \
WAZUH_DASHBOARD_USER=admin \
WAZUH_DASHBOARD_PASSWORD=SecretPassword \
sh apply-dashboard-default-columns.sh
```

The script sets `defaultColumns` so prompt text, event action, harness, model,
repository, command, file, and session fields appear by default. Wazuh's
default alert index maps Beacon command and file details as `data.command` and
`data.file`.
