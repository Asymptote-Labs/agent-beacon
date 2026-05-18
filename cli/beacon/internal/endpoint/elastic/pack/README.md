# Beacon Endpoint Agent Elastic Pack

This pack forwards Beacon endpoint JSONL events into Elasticsearch with Filebeat
or standalone Elastic Agent. Beacon still writes one local source of truth:
`runtime.jsonl`. Elastic credentials and cluster URLs live in the forwarding
tool, not in Beacon endpoint configuration.

## What This Pack Includes

- `filebeat.yml`: Filebeat filestream input for Beacon endpoint JSONL.
- `elastic-agent-standalone.yml`: standalone Elastic Agent input for the same
  log file.
- `ilm-policy.json`, `component-template-*.json`, `index-template.json`, and
  `ingest-pipeline.json`: Elasticsearch assets for `logs-beacon.endpoint-*`.
- `kibana-assets.ndjson`: starter Kibana data view and Discover saved search.
- `docker-compose.yml`: local Elasticsearch, Kibana, and Filebeat stack for
  development and validation.
- `sample-event.jsonl`: sample Beacon event for pipeline simulation.

Minimum versions: Elasticsearch, Kibana, Filebeat, and Elastic Agent 8.x.

## Local Elastic Stack

Generate the pack and start the bundled local development stack:

```bash
beacon endpoint install
beacon endpoint elastic install-pack --output ./beacon-elastic-pack
beacon endpoint elastic up --pack-dir ./beacon-elastic-pack
```

The pack includes a Beacon data view and a starter Discover saved search. The
stack binds Elasticsearch and Kibana to loopback:

- Elasticsearch: `http://localhost:9200`
- Kibana: `http://localhost:5601`

If those ports are already in use, set `BEACON_ELASTIC_ES_PORT` or
`BEACON_ELASTIC_KIBANA_PORT` before running `elastic up`.

The Docker stack loads the ILM policy, component templates, index template,
ingest pipeline, and starter Kibana saved objects before Filebeat ships events.
It also mounts the host directory that contains Beacon's configured
`runtime.jsonl`, so the generated `filebeat.yml` can tail the same absolute log
path inside the Filebeat container.

Open Kibana, select the `Beacon Endpoint Events` data view, and use Discover to
verify events. If you need a test record before agent activity arrives, write a
validation event with another integration command such as
`beacon endpoint wazuh validate`, or copy `sample-event.jsonl` into your Beacon
runtime log during local testing.

Stop it with:

```bash
beacon endpoint elastic down --pack-dir ./beacon-elastic-pack
```

`elastic up` and `elastic down` are macOS-oriented local validation helpers. For
Linux endpoints or production deployments, use the generated Filebeat or Elastic
Agent configuration with your normal service manager.

## Hosted Or Self-Managed Elastic

Install the JSON assets in this order:

```bash
cd beacon-elastic-pack

curl -X PUT "$ES_HOSTS/_ilm/policy/beacon-endpoint" \
  -H "Authorization: ApiKey $ES_API_KEY" \
  -H 'Content-Type: application/json' \
  --data-binary @ilm-policy.json

curl -X PUT "$ES_HOSTS/_component_template/beacon-endpoint-mappings" \
  -H "Authorization: ApiKey $ES_API_KEY" \
  -H 'Content-Type: application/json' \
  --data-binary @component-template-mappings.json

curl -X PUT "$ES_HOSTS/_component_template/beacon-endpoint-settings" \
  -H "Authorization: ApiKey $ES_API_KEY" \
  -H 'Content-Type: application/json' \
  --data-binary @component-template-settings.json

curl -X PUT "$ES_HOSTS/_index_template/beacon-endpoint" \
  -H "Authorization: ApiKey $ES_API_KEY" \
  -H 'Content-Type: application/json' \
  --data-binary @index-template.json

curl -X PUT "$ES_HOSTS/_ingest/pipeline/beacon-endpoint" \
  -H "Authorization: ApiKey $ES_API_KEY" \
  -H 'Content-Type: application/json' \
  --data-binary @ingest-pipeline.json
```

For Kibana, import `kibana-assets.ndjson` through Stack Management or the saved
objects import API.

Configure Filebeat with `filebeat.yml`, setting:

- `ES_HOSTS`, for example `http://localhost:9200` or an Elastic Cloud URL.
- One authentication method for secured clusters: uncomment `api_key`, or
  uncomment `username` and `password`.
- `ES_SSL_VERIFY` when your cluster needs a non-default TLS verification mode.
- `ssl.certificate_authorities` in the generated YAML when your cluster needs a
  custom CA bundle.

For Elastic Cloud, `ES_HOSTS` is the deployment Elasticsearch endpoint, for
example `https://example.es.us-east-1.aws.elastic.cloud:443`. Create an API key
with the privileges below, then run Filebeat with:

```bash
export ES_HOSTS="https://example.es.us-east-1.aws.elastic.cloud:443"
export ES_API_KEY="base64-encoded-api-key"
filebeat -e -c ./filebeat.yml
```

For a self-managed secured cluster, the same environment variables work. If you
use username/password auth instead, uncomment `username` and `password` in the
generated config and provide `ES_USERNAME` and `ES_PASSWORD`.

To use standalone Elastic Agent instead of Filebeat, apply the same `ES_HOSTS`
and authentication environment variables to `elastic-agent-standalone.yml` and
run Elastic Agent in standalone mode.

## Minimal Elasticsearch Role

Filebeat needs cluster `monitor` plus `auto_configure`, `create_doc`, and
`view_index_metadata` on `logs-beacon.endpoint-*`.

## Pipeline Simulation

Convert the sample JSONL into a simulate request and post it to Elasticsearch:

```bash
awk '{print "{\"docs\":[{\"_source\":" $0 "}]}"}' sample-event.jsonl | \
  curl -X POST "$ES_HOSTS/_ingest/pipeline/beacon-endpoint/_simulate" \
    -H "Authorization: ApiKey $ES_API_KEY" \
    -H 'Content-Type: application/json' \
    --data-binary @-
```

For an unsecured local development cluster, omit the `Authorization` header.
