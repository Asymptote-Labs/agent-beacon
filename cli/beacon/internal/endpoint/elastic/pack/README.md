# Beacon Endpoint Agent Elastic Pack

This pack forwards Beacon endpoint JSONL events into Elasticsearch with Filebeat
or standalone Elastic Agent. Beacon still writes one local source of truth:
`runtime.jsonl`. Elastic credentials and cluster URLs live in the forwarding
tool, not in Beacon endpoint configuration.

## Local Stack

Generate the pack and start the bundled local development stack:

```bash
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

Validate ingestion with the ECS-adjacent fields produced by the ingest pipeline:

```bash
curl "$ES_HOSTS/logs-beacon.endpoint-*/_search?q=beacon.product:endpoint-agent"
curl "$ES_HOSTS/logs-beacon.endpoint-*/_search?q=beacon.prompt.text:%22Beacon%20E2E%22"
curl "$ES_HOSTS/logs-beacon.endpoint-*/_search?q=beacon.harness.name:cursor"
```

Stop it with:

```bash
beacon endpoint elastic down --pack-dir ./beacon-elastic-pack
```

## Existing Elastic Deployments

Install the JSON assets in this order:

```bash
curl -X PUT "$ES_HOSTS/_ilm/policy/beacon-endpoint" -H 'Content-Type: application/json' --data-binary @ilm-policy.json
curl -X PUT "$ES_HOSTS/_component_template/beacon-endpoint-mappings" -H 'Content-Type: application/json' --data-binary @component-template-mappings.json
curl -X PUT "$ES_HOSTS/_component_template/beacon-endpoint-settings" -H 'Content-Type: application/json' --data-binary @component-template-settings.json
curl -X PUT "$ES_HOSTS/_index_template/beacon-endpoint" -H 'Content-Type: application/json' --data-binary @index-template.json
curl -X PUT "$ES_HOSTS/_ingest/pipeline/beacon-endpoint" -H 'Content-Type: application/json' --data-binary @ingest-pipeline.json
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

Minimum versions: Elasticsearch, Kibana, and Filebeat 8.x.

## Minimal Elasticsearch Role

Filebeat needs cluster `monitor` plus `auto_configure`, `create_doc`, and
`view_index_metadata` on `logs-beacon.endpoint-*`.

## Pipeline Simulation

Convert the sample JSONL into a simulate request and post it to Elasticsearch:

```bash
awk 'BEGIN{printf "{\"docs\":["} NR>1{printf ","} {printf "{\"_source\":" $0 "}"} END{print "]}"}' sample-event.jsonl | \
  curl -X POST "$ES_HOSTS/_ingest/pipeline/beacon-endpoint/_simulate" \
    -H 'Content-Type: application/json' \
    --data-binary @-
```

If you run `beacon endpoint elastic up`, the host log directory is mounted into
the Filebeat container at the same absolute path so the generated `filebeat.yml`
can tail the configured Beacon log path unchanged.
