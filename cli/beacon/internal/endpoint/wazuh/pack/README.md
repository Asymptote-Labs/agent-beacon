# Beacon Endpoint Agent Wazuh Pack

Install `beacon-rules.xml` on the Wazuh manager and add the localfile
snippet to the Wazuh agent configuration on managed endpoints.

The generated `ossec-localfile.xml` should point at the endpoint runtime log
configured on the endpoint.

Validate locally with:

```bash
beacon endpoint wazuh validate
```
