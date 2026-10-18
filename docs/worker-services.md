# EWSS-23: matching and analytics worker services

AS-MS-I-K-01 implements the two Python applications in EWSS-14. The workers share
versioned contracts and transport code, but run in separate processes with separate
settings, credentials, consumer groups and probes. Neither runtime image includes
a MySQL driver or database credentials. Both call authenticated Batch Workflow APIs;
Go retains authoritative transactions and idempotency checks.

| Application | Start command                     | Docker build target | Kafka consumer group      |
| ----------- | --------------------------------- | ------------------- | ------------------------- |
| Matching    | `python -m matcher run-matching`  | `matching-worker`   | `matching-worker-v1`      |
| Analytics   | `python -m matcher run-analytics` | `analytics-worker`  | `analytics-processing-v1` |

Keep these existing group IDs during the split to retain committed offsets. Every
replica of one application uses that application's group ID. The two applications
must use different group IDs, otherwise they would divide events instead of each
receiving the lifecycle stream. Both consume `ewaste.batch.events`, keyed by batch
ID; matching handles `RequestSubmitted`, analytics handles `RecyclingCompleted`.
Matching alone publishes quarantine records. Analytics retains repair-and-retry
behavior for invalid records; this change introduces no analytics DLQ.

## Configuration and startup

Use `src/matcher/matching.env.example` and `src/matcher/analytics.env.example` as
templates for the ignored `matching.env` and `analytics.env` files beside them.
Supply real credentials through the deployment secret store. Set each facade URL
to Batch Workflow's internal origin. The existing authenticated analytics API
aliases remain compatible with the worker's requests.

Matching reads `MATCHER_*` settings; standalone analytics reads `ANALYTICS_*` settings.
Each has independent broker/config-file, topic, group, offset reset, poll/session
timeout, processing concurrency, HTTP timeout, response/record limits, retry limits
and health port. Analytics needs only its analytics service token; matching needs
only its workload JWT configuration or token file. There are no cross-worker token,
DLQ or numeric-setting dependencies.

Both support shared `KAFKA_BOOTSTRAP_SERVERS`, `KAFKA_CONNECTION_STRING` and
`KAFKA_TOPIC_BATCH_EVENTS` aliases when the worker-specific settings are absent.
Worker-specific broker values take precedence. Analytics does not inherit the
matching config file or its local-test security switch. Plaintext requires the
selected worker's explicit `*_LOCAL_TEST=1`; use TLS/SASL and HTTPS otherwise.

From the repository root, against provisioned Kafka and Batch Workflow:

```sh
docker compose -f src/matcher/compose.yml up -d --build --wait
docker compose -f src/matcher/compose.yml ps
```

The Compose file exposes no host ports. Each container serves `/healthz` and
`/readyz` on its own configured port. Failure or restart of one worker does not
stop the other. Readiness requires live broker polling and no blocked delivery;
an idle readiness check alone does not prove a successful business transaction.

For Azure, build each target and deploy it to its own internal Container App with
the corresponding settings and probes. This task supplies the runtime targets and
local execution evidence; creating/updating Azure apps and cloud deployment wiring
belongs to the cloud deployment tasks. Existing CD/Terraform deployments still use
the legacy image target unless explicitly switched.

## Compatibility during rollout

`python -m matcher run` and a Docker build without `--target` preserve combined
startup. Only this legacy command honors `ANALYTICS_ENABLED` and shares matching
transport settings with analytics. Explicit `run-matching` never starts analytics;
explicit `run-analytics` always validates and starts analytics, even if a stale
`ANALYTICS_ENABLED=false` is present. Remove the old combined app after switching
to the separate apps so its consumers do not remain unplanned group members.

## Verification

```sh
./scripts/test-matcher.sh
./scripts/test-worker-services.sh
```

The first runs the existing Go, PyTest, Kafka and MySQL regression suite, including
lost-response replay and transaction rollback checks. The second creates a separate
disposable environment and verifies the actual Docker targets:

1. Start two matching replicas and two analytics replicas over three partitions.
2. Check both consumer groups have two active members with partition ownership.
3. Drive three real HTTP lifecycles to completion through the Go API.
4. Replay each original matching and analytics source on all three partitions to
   exercise concurrent duplicate requests, then check committed offsets and SQL.
5. Kill one replica per application, verify surviving consumers take all partitions,
   replay again, restore the replicas, and repeat the checks after rebalancing.
6. Assert batch versions and row counts remain unchanged: one matching decision,
   one analytics result and one completion outbox event per batch, without extra
   candidates, anomalies, commands or audit rows.

Cross-partition duplicates are deliberate fault injection; production producers
continue to partition by batch ID. Kafka remains at least once. Durable replay
comes from the existing stable event/command identity and Go/MySQL transactions.

PyTest JUnit files, per-phase group assignments, offsets, SQL snapshots, app status,
image IDs, source checksums and logs are retained under `artifacts/worker-services/`.
The GitHub matching workflow runs both verification scripts and uploads their
evidence. Each script removes only its own disposable resources by default.
Use `KEEP_WORKER_APPS=1 ./scripts/test-worker-services.sh` to retain healthy local
apps after success; the evidence directory contains the exact cleanup command.
