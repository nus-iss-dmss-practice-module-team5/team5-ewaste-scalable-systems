# Matcher, API façade and persistence

Matching and anomaly/impact processing can run as separate applications using
`python -m matcher run-matching` and `python -m matcher run-analytics`. See the
[EWSS-23 worker service runbook](../../docs/worker-services.md) for independent
settings, Docker targets and multi-consumer replay verification. The original
`run` command retains combined startup for existing deployments. See
[the analytics worker and evidence runbook](../../docs/analytics-worker.md) for the
`d3-v1` rules, independent consumer group, service authentication and recovery tests.

This slice implements the approved C2/Task 5 path:

```text
Go SubmitBatch → MySQL event_outbox → Go relay → ewaste.batch.events
                                                   ↓ RequestSubmitted
                                      Python deterministic binary-v1 matcher
                                                   ↓ authenticated Go API
                               decision + all candidates + audit + outbox
                                                   ↓ Go relay
                                   MatchingCompleted on ewaste.batch.events
```

Python evaluates frozen inputs and publishes sanitised quarantine records. The Go
[matching façade](../backend/internal/matching/README.md) owns authoritative reads,
source-event verification, result validation and atomic MySQL writes. Python has
no database driver or credentials in its runtime image; PyMySQL exists only in the
test image. No approved SQL migration is changed by this task.

## Prerequisites and scope

This slice is integrated with the backend, Event Hubs infrastructure and C1–C4 SQL
work present on `feature/EWCSB-129` at `6e8b85e` (migrations **001–025**, master
changelog and synthetic seed data). The original patch targeted backend PR #41;
its Kafka TLS/SASL configuration remains compatible with the merged backend.

The existing workflow-read controllers own public opportunity routes. The matcher
uses those routes and preserves their response shape and ordering, with scope
resolved from current database membership rather than stale JWT role/org claims.
The internal matching façade is registered separately, avoiding duplicate routes.

CD reuses the current Terraform resources: Event Hubs namespace/topics, managed
identity, ACR, and `aca-ewaste-{env}-analytics`. It requires those resources to
exist and preserves ingress, identity, registry and scaling settings.

Terraform and CD both require the environment's same `MATCHER_SIGNING_SECRET`.
Terraform retains the API facade and worker configuration on subsequent applies;
CD deploys immutable images and restarts revisions after updating secrets. For
the September 26 invalid configuration IDs and missing environment settings,
follow the [dev recovery guide](../../database/maintenance/README.md).

The original approved schema/API files and two golden fixtures remain unchanged.
Their design-document implementation status is historical. Implemented internal
routes and public opportunity reads are listed in the façade README.

## Run all verification

From the repository root with Docker Compose available:

```sh
./scripts/test-matcher.sh
```

The script verifies approved-contract checksums and embedded Go schema copies,
builds the images, applies the real master changelog with synthetic seeds, runs
the entire Go suite with MySQL integration checks, then runs Python unit,
HTTP-protocol, real Kafka and real Go/MySQL acceptance tests. Go 1.26, Python
3.12.12, Kafka 4.1.0, MySQL 8.4 and Liquibase 4.29.2 are retained. Redis uses 7.4.

Everything runs in a unique disposable Compose project with no host ports. Its
containers and volumes are removed on exit. Logs, migration results, source
checksums and SQL evidence remain under `artifacts/matcher/<run>/`. Fixture keys,
passwords and trigger-creation settings in `testing/compose.yml` are local only.
The GitHub `matcher.yml` workflow runs this same script on relevant PRs, dev/main
pushes and manual dispatch. It migrates its own isolated database; it does not
wait for the deployment migration workflow.

| Acceptance criterion | Executable evidence |
|---|---|
| Identical inputs give identical results | Both approved goldens; independent Go/Python parity; reordered inputs, exact decimal boundaries, timezone and process invariance |
| Envelope/topic/key/order conform | Unchanged v1 schemas, full Go producer validation, PR #41 SubmitBatch payload compatibility, real broker/manual-offset checks, delayed retry and quarantined batch ordering checks |
| Retries do not duplicate durable effects | Eight concurrent Go commits, durable replay after restart/later lifecycle changes, lost HTTP result response, duplicate Kafka delivery and consumer restart; one decision/audit/outbox intent |
| Failure recovery is observable | Actual MySQL outbox-insert fault rolls back every write; retry succeeds; API/broker/offset failures pause and recover; lease loss cancels publication; invalid configuration remains retryable |
| Source identity is retained | Real SQL assertions for batch ID, original trigger event, frozen snapshot and business correlation; persisted MatchingCompleted validates against the approved schema |

The HTTP protocol double has a separate test topic. Its observations are labelled
and are not used as SQL persistence evidence. Golden inputs have fixed identities,
times and expected outputs. Live end-to-end fixtures use current submission time
to stay inside the submission window; durable replay preserves the frozen values.

## Delivery and recovery

`KafkaRunner` disables automatic offset storage/commit, processes each partition
in offset order, and keeps polling while API work is pending. It commits only the
resolved record's `offset + 1`. Revoked work cannot acknowledge offsets. Completion
means a durable COMMITTED/SKIPPED response or acknowledged sanitised quarantine.
A bare conflict or an API error never acknowledges a source record.

The command key is `REQUEST_SUBMITTED:<event_id>` under the stable actor scope
`service:matching-worker`. Go verifies the complete source against retained
`event_outbox` content before preparation. Response loss resolves the same run
before retrying identical output. Only explicit STALE_CONTEXT allows a fenced
refresh. T2 independently validates every predicate and candidate, and persists
all true and false results. Matching never reserves capacity. NO_MATCH leaves the
batch status/version unchanged.

The Go relay uses the backend's Redis ownership lease, renews it independently of
broker calls and cancels work on lease loss. Selection permits one outstanding
head per batch across topics; delayed retries cannot be bypassed, and quarantined
batches pause pending reviewed disposition. Sort keys are operational, not proof
of historical commit order. No cross-topic or total-order guarantee is claimed.
Temporary broker failures retain PENDING intent indefinitely with bounded retry
delay. Invalid persisted contracts are quarantined without rewriting their data.

Business topic `ewaste.batch.events` carries UTF-8 JSON with `schema_version=1`,
keyed by lowercase batch UUID. Python publishes only to
`ewaste.batch.events.matching.dlq.v1`, keyed by SHA-256 `quarantine_id`.
MatchingCompleted and unrelated events are observable skips in the matcher.
Unknown fields, raw payload bytes, credentials and user notes never enter DLQ
records. Source coordinates and the original-byte hash are retained.

Kafka delivery remains at least once. A crash after broker acceptance before SQL
marking can duplicate a record; stable event IDs and command replay prevent extra
matching results or outbox intents. Producer idempotence does not make Kafka and
MySQL one transaction. External consumers that create other business effects
still need their own durable deduplication.

## Runtime configuration and authentication

For standalone local configuration, start with `.env.example`. Run
`python -m matcher run` after setting its required values. For pure evaluation,
set `PYTHONPATH=src/matcher` and pipe a MatchingInputV1 object to
`python -m matcher evaluate`. Golden files wrap that object under `input`.

Kafka settings can come from `MATCHER_KAFKA_CONFIG_FILE` or the supplied infra's
`KAFKA_BOOTSTRAP_SERVERS` and `KAFKA_CONNECTION_STRING`. Event Hubs uses SASL_SSL,
PLAIN and username `$ConnectionString`; missing credentials fail startup.
The Go publisher uses PR #41's `EWASTE_KAFKA_TLS_ENABLED`,
`EWASTE_KAFKA_SASL_MECHANISM`, `EWASTE_KAFKA_SASL_USERNAME` and the
`KAFKA_CONNECTION_STRING` password alias. Set TLS to true, mechanism to PLAIN and
username to the literal `$ConnectionString`. The old matcher-specific Go
`EWASTE_KAFKA_SECURITY_PROTOCOL` setting is no longer used. PR #41's configuration
loader and existing tests are retained unchanged.

Plaintext HTTP/Kafka requires the explicit local-test switch. The worker never
creates deployment topics. It accepts the infra's KAFKA_TOPIC_BATCH_EVENTS,
KAFKA_TOPIC_DLQ and KAFKA_CONSUMER_GROUP aliases when MATCHER_* equivalents are absent.

The provided Azure integration uses a dedicated application signing secret, not
Entra identity for API authentication. Set the GitHub Environment secret
`MATCHER_SIGNING_SECRET` to a securely generated value of at least 32 bytes, unique
per environment and distinct from user-login secrets. CD stores it as a Container
Apps secret in API and worker. Python mints a five-minute HS256 JWT per HTTP request;
Go verifies issuer, audience, expiry, subject and scopes. This remains configurable:
externally supplied compatible tokens may instead use MATCHER_TOKEN_FILE (reread
per request) or MATCHER_BEARER_TOKEN. These modes do not accept Entra tokens without
an additional verifier implementation.

For manual dedicated-key setup, configure these paired values:

| Python worker | Go API |
|---|---|
| MATCHER_SIGNING_SECRET or MATCHER_SIGNING_SECRET_FILE | EWASTE_MATCHING_SIGNING_SECRET or EWASTE_MATCHING_SIGNING_SECRET_FILE |
| MATCHER_TOKEN_ISSUER | EWASTE_MATCHING_ISSUER |
| MATCHER_TOKEN_AUDIENCE | EWASTE_MATCHING_AUDIENCE |
| MATCHER_FACADE_URL=https://API-host | EWASTE_MATCHING_ENABLED=true |

Worker keys cannot authorise explicit reruns. That optional operator path needs a
separate signing key and scope, as documented in the façade README.

## Supplied Azure deployment

The updated CD workflow builds `src/matcher/Dockerfile` from the repository root
and deploys its digest in the existing analytics slot. It replaces the old demo
worker and removes demo publish/read smoke endpoints. Rollback requires an image
labelled `ewaste.component=matcher`; legacy analytics images are rejected.

`scripts/deploy-matcher.sh` waits for the exact namespace and all six hubs, sets
TLS/SASL credentials for **both** Go and Python, configures the façade, checks an
authenticated read-only command lookup, and requires worker `/readyz` to succeed
in dev. Staging and production retain internal ingress: the authenticated deployment
step waits for the exact new revision to be Running and Healthy through the ACA
control plane. That check establishes platform health, not Kafka readiness;
`/readyz` remains available from inside the ACA environment.
The worker starts with group `matching-worker-v1` and `earliest`, avoiding offsets
previously auto-committed by the demo. Existing valid events must still have their
original outbox facts and matching command history available for verification.

The supplied IaC provisions one partition and one day of retention per hub. This
patch does not change those settings. Retention bounds what Kafka can replay;
it is not extended by MySQL replay history. Finish the existing migration workflow
and provision the approved active matching rule set/recycler configuration before
enabling matching. Those deployment workflows are independent. CI's synthetic
fixture configuration must not be copied into production.

Worker `/healthz` reflects recent polling; `/readyz` also requires current broker/
group statistics and no locally blocked delivery. These endpoints disclose only
aggregate status. Readiness alone does not prove an idle worker's database writes;
the separate authenticated API check and local integration suite serve different
purposes. The existing ingress is retained; no event read/publish endpoints exist.
Matcher-specific environment settings and secret bindings are applied by CD with
`--set-env-vars`, retaining unrelated IaC settings. Run CD after an IaC apply that
reconciles container environment variables.

Watch structured `delivery_paused`, `consumer_error`, `offset_commit_failed`,
`matching_request_failed`, outbox quarantine/retry logs, and consumer lag. Fix the
cause and resume with the same group/event identity. Do not delete replay records,
fabricate new submissions or blindly reset offsets. Redrive tooling and alert
thresholds are outside this task.

Azure configuration has been checked against the supplied source and
[Microsoft's Kafka settings](https://learn.microsoft.com/en-us/azure/event-hubs/apache-kafka-configurations)
and [Container Apps CLI reference](https://learn.microsoft.com/en-us/cli/azure/containerapp).
No Azure deployment was executed as part of this verification. The included evidence
is from real local Kafka/MySQL/Redis containers.
