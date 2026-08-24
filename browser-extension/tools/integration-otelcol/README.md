# Integration collector (opt-in)

`e2e/integration.beacon.spec.ts` (run via `npm run test:integration`, gated on `RUN_INTEGRATION=1`)
spawns the **real** `/opt/beacon/bin/beacon-otelcol` with a generated test config that:

- listens on isolated OTLP ports (so it never touches the live `127.0.0.1:4318` collector), and
- writes via the `beaconjson` exporter to a **temp** `runtime.jsonl` (never `/var/log`).

It then POSTs the exact OTLP envelope our `turnToEnvelope()` produces and asserts the written line
deserializes to a valid beacon `Event` (schema_version, event.action, harness.name, session.id,
gen_ai.*, prompt.text) with vendor/product/timestamp filled by the exporter.

This is the only layer that proves our OTLP → real `converter.go` → `Event` contract. It is
advisory (kept out of the default unattended loop) because it depends on the installed binary.
