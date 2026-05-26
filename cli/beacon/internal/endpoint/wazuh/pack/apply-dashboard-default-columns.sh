#!/bin/sh
set -eu

DASHBOARD_URL="${WAZUH_DASHBOARD_URL:-https://localhost}"
DASHBOARD_USER="${WAZUH_DASHBOARD_USER:-admin}"
DASHBOARD_PASSWORD="${WAZUH_DASHBOARD_PASSWORD:-SecretPassword}"

curl -sk \
  -u "${DASHBOARD_USER}:${DASHBOARD_PASSWORD}" \
  -X POST \
  -H 'osd-xsrf: beacon' \
  -H 'Content-Type: application/json' \
  "${DASHBOARD_URL}/api/opensearch-dashboards/settings/defaultColumns" \
  -d '{"value":["timestamp","agent.name","rule.description","rule.level","rule.id","data.event.action","data.prompt.text","data.message","data.harness.name","data.model","data.repository","data.command","data.file","data.session.id","data.session.working_directory"]}'

printf '\nBeacon Wazuh default columns applied to %s\n' "${DASHBOARD_URL}"
