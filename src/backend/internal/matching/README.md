# Matching API façade

This package implements the four internal C2 matching routes. Public opportunity
reads use the merged backend's workflow-read controller/service/repository rather
than registering duplicate routes. The previously approved migrations 001–025
are required; no new tables or changes to approved migrations are introduced.

## Internal routes

| Route | Purpose | Workload permission |
|---|---|---|
| `POST /internal/v1/matching/runs` | Validate the retained source event and durably freeze inputs | `matching.execute` |
| `GET /internal/v1/matching/runs/{run_id}` | Retrieve the saved preparation or terminal result | `matching.read` |
| `POST /internal/v1/matching/runs/{run_id}/refresh` | Refresh only an explicitly stale generation | `matching.execute` |
| `POST /internal/v1/matching/runs/{run_id}/result` | Validate and atomically persist the full result | `matching.execute` |

The wire bodies follow `contracts/matching/api/matching-api.yaml`. Internal
responses intentionally have no public `data` wrapper: the Python client consumes
the approved PREPARED/COMPLETED and COMMITTED/SKIPPED protocol directly.

`command_idempotency.response_json` retains the original request and frozen
context. Its existing `IN_PROGRESS`/`COMPLETED` states remain unchanged. The
actor is always `service:matching-worker`, independent of replica identity.

T1 verifies the original event against immutable `event_outbox` content and the
current submitted batch. T2 rechecks the complete configuration, independently
recomputes every M1–M5 predicate/evidence value, and writes the decision, all true
and false candidate rows, conditional batch transition, lifecycle audit, outbox
intent, and stable response in one MySQL transaction. `NO_MATCH` leaves the batch
status/version unchanged. Matching never reserves capacity.

An identical completed replay is resolved before inspecting the batch's later
lifecycle. A conflicting request/result is rejected. A state conflict after a
valid source is persisted as a terminal skip; a bare error never acknowledges a
Kafka delivery. A stale result records a refresh fence under the command lock;
refresh requires that saved fence and the old input hash.

Full configuration scans use `SELECT ... FOR UPDATE` in explicit MySQL
REPEATABLE READ transactions. Next-key locks cover both existing rows and gaps,
including newly approved organisations or previously absent configuration.
This favors correctness for the project's small configuration set; it serializes
configuration writers and may need a versioned configuration-head design at
larger scale. Deadlocks/dependency failures return retryable 503 responses.

## Workload authentication

Enable with `EWASTE_MATCHING_ENABLED=true`. Configure the issuer, audience and a
dedicated signing secret using `EWASTE_MATCHING_ISSUER`,
`EWASTE_MATCHING_AUDIENCE`, and `EWASTE_MATCHING_SIGNING_SECRET` (or the
`EWASTE_MATCHING_SIGNING_SECRET_FILE` file). Never reuse the user-login key.
HS256 tokens require a valid signature, expiry, issuer, audience, subject
`matching-worker`, and route scope. The worker mints five-minute tokens on each request.
`EXPLICIT_RUN` requires a token with `kid=operator`, subject `matching-operator`,
`matching.execute matching.rerun`, signed with the separate optional
`EWASTE_MATCHING_OPERATOR_SIGNING_SECRET`. A worker key cannot grant that permission,
even if a token claims the rerun scope. Operator reruns are disabled by default. Default body
limit is 16 MiB, configurable up to 64 MiB. No credentials are logged.

## Recycler reads

`GET /api/v1/opportunities` and `GET /api/v1/opportunities/{batch_id}` use the
existing active-session middleware plus current database role/organisation
checks. Only the caller's eligible row for the current MATCHED batch version and
claim epoch is returned. Other organisations, stale decisions, and non-matched
results cannot be read. No competitor evidence or capacity is exposed.

These public reads retain PR #41's `data`, `page`, `page_size`, `total_count`,
and `correlation_id` response convention. The older companion's cursor proposal
is not used by the latest backend API. Internal matching wire contracts and the
approved JSON schemas remain unchanged.

## Verification

Run `./scripts/test-matcher.sh` from the repository root. The Go tests verify
cross-language golden parity, authentication failures, source forgery, concurrent
commits, replay after restart, stale candidate-set refresh, `NO_MATCH`, scoped
reads, and rollback after an actual MySQL trigger rejects the outbox insertion.
The Python integration tests then consume the Go relay's real Kafka records and
verify persisted MySQL results and downstream event publication.

Schemas under `../matchingcontract/schemas/` are byte-preserved copies for Go embedding. The compiler
only translates the redundant end-of-string negative lookahead in memory because
Go's regular-expression `$` already denotes absolute end of text. No network
schema resolution is allowed.
