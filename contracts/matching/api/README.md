# Matching API facade — v1 draft

Implementation owner: **Unassigned**. The OpenAPI is design evidence, not deployed API code. Issuer/audience, route names, scopes, run JSON convention and transport limits need implementation review.

## Boundary

- Worker: Kafka adapter, pure deterministic evaluation, authenticated facade calls; no MySQL or Redis access.
- Go facade: authenticate, freeze/validate inputs, allocate IDs, transact MySQL, expose only caller-scoped opportunities.
- No claim, assignment, collector recovery or capacity reservation in these six routes.
- `FAILED_COLLECTION -> APPROVED` retains the recycler/claim/epoch; a new collector later chooses it. It is not a matching trigger. Reject `trigger_type=RECOVERY`.

## Two distinct correlation values

| Value | Contract | Rule |
|---|---|---|
| Business `correlation_id` | Body, Kafka envelope, saved run/audit/outbox; 1–128 characters | Preserve the original event value exactly. For submission-triggered preparation, request and `original_event` must agree. |
| HTTP `X-Correlation-ID` | Existing middleware caps raw header at 100 bytes; draft adapter uses printable non-whitespace ASCII | Reuse business ID only when header-safe and unchanged by trimming; otherwise use a new UUID transport ID. |
| Error `transport_correlation_id` | Trace for this HTTP attempt | May differ on retry; never part of the durable request hash or matching input hash. |

Do not truncate the business ID or reject an otherwise valid 101–128-character Kafka ID. A transport trace change cannot create a new run. Log only allowlisted trace mappings; no tokens/snapshots. Error `correlation_id` is returned only when validated and authorised.

## Run response protocol

| Phase | Body | Persistence meaning |
|---|---|---|
| `PREPARED` | `run_id`, `prepared_context` | Existing `command_idempotency` is `IN_PROGRESS`; immutable input is durable. No decision row. |
| `COMPLETED` / `COMMITTED` | Stable decision IDs, counts, outcome, original committed batch status/version | Atomic T2 completed; identical replay returns the original result. |
| `COMPLETED` / `SKIPPED` | `code=STATE_CONFLICT`, run/batch/trace | Valid authenticated trigger is durably terminal; no matching decision or domain mutation. |

`phase`/`disposition` are response/JSON values, not new SQL status values. The DB command states remain `IN_PROGRESS` and `COMPLETED`. An HTTP 409 by itself is not permission to acknowledge Kafka: confirm a durable skip in the response or by GET. Conflicting idempotency content must never overwrite the original command.

The facade validates event identity/content against immutable `event_outbox` plus current batch. Never trust an arbitrary Kafka-shaped body. Store the original trigger request hash separately from result hash. Complete replay lookup happens before the ordinary `SUBMITTED` guard.

## Validation beyond structural schemas

- Path/body run IDs; trigger/source event/batch/version/epoch/trace equality.
- `Idempotency-Key = trigger_type + ':' + trigger_id`; actor/command scope, no global key uniqueness.
- Full approved candidate set, exact saved hashes/generation, rules, counts, reason order and evidence.
- `eligible_count <= evaluated_count`; MATCHED means positive eligible count and original batch status MATCHED; NO_MATCH means zero eligible count and original batch status SUBMITTED.
- Current approved-organisation set plus configuration versions/absent rows must still agree before commit. No incomplete or truncated candidate set.
- Public list/detail exclude competitor capacity/reasons and return only current eligible rows in caller organisation scope.

The companion uses external JSON Schema references. Resolve them relative to `matching-api.yaml`; keep `api`, `contracts` and `kafka` sibling directories together. No live endpoint or authentication test is claimed.
