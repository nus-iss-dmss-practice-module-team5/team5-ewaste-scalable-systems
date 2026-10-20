# Processing storage and migration evidence

This corrects the supplied, not-yet-deployed migrations 026–030. Do not replace
an already executed changeset, clear checksums, mark failed migrations as run, or
delete lifecycle rows. See the recovery procedure below if these IDs already ran.
The main changelog's existing 026–030 includes are retained.

## Storage contract

- Receipt: one per batch and command; approved category, integer count 1–100000,
  received kg 0.10–50000.00. `command_id` and `correlation_id` match the Go receipt
  model; version defaults to 1. No extra mandatory condition or method field.
- Treatment: one per batch/command, linked to a receipt's batch, facility, version
  and exact weight. Supply all three kg values or none. Values are nonnegative,
  sum cannot exceed received weight. Generated `unknown_kg` is the remainder, or
  the full received weight for an absent outcome. Do not include generated columns
  in INSERT/UPDATE statements. Evidence is optional; its composite FK enforces
  the same batch, owning facility and TREATMENT stage.
- Result: `metric_id` stores D4's `result_id`. Persist source event/batch version,
  receipt/treatment IDs and versions, correlation, command, immutable rule version,
  frozen input JSON and canonical input/result SHA-256 values. One authoritative
  result per batch and source event. `diverted_kg = reused_kg + recycled_kg`;
  disposed is separate. Unknown and diverted are generated, never client inputs.
  COMPLETE means fully allocated, PARTIAL means positive remainder, MISSING means
  all three treatment values and diverted are NULL and all received kg is unknown.
- Anomalies reference the result and batch; `(metric_id, anomaly_code)` is unique.
  Read rule_version by joining the immutable result. Flags retain the D3 order in
  API/event output (CATEGORY, COUNT, WEIGHT, MISSING_OUTCOME, UNALLOCATED_WEIGHT).

Go still owns authorization, valid transitions, optimistic versions, canonical
hash verification, frozen-value comparison, source event type, replay response and
atomic result/state/audit/outbox/command persistence. Same replay body returns the
saved result; different body is rejected. Hash format and uniqueness do not by
themselves prove a trusted result. Reject excess precision BEFORE DECIMAL coercion
and fractional counts BEFORE integer coercion. SQL cannot preserve lexical input
precision. Receipt/treatment rows are immutable in this version; use a new
reviewed workflow if corrections/recalculation become necessary.

## Repeatable local checks

Run `bash scripts/test-persistence.sh` from the repository root with Docker running.
The existing MySQL/Liquibase versions are retained. The runner creates disposable
containers and removes them on exit; it never connects to Azure.

The new processing section runs a clean schema, then upgrades a populated C4
schema with real SQL claim/assignment/handoff/audit history from the existing
suite. It compares complete legacy row dumps and old changelog records before and
after, exercises exact FK/index definitions, executes valid/invalid processing
SQL on both databases twice, and checks migration repeat plus zero fixture residue.
Find logs, TSV results, before/after dumps and empty preservation diffs beneath
`artifacts/persistence/<run>/processing/` (or `PERSISTENCE_EVIDENCE_DIR`). Both SQL
files under database/tests are required. A FAIL row or unexpected MySQL error
fails the process. The four golden cases cover complete, discrepant partial,
all-absent and explicit-zero/lower-weight boundary outcomes. SQL fixtures are
synthetic constraint tests, not evidence that API RBAC or Kafka delivery passed.

Keep migration logs, schema/fixture TSVs, preservation diffs and final result.txt
with the release evidence. Never upload credentials or real customer database dumps.

## Safe repair and release order

1. Before deployment, inspect DATABASECHANGELOG for EWCSB3-026 through EWCSB3-030
   and inventory tables/indexes. Take and verify a restorable database backup.
2. If none ran and no processing tables exist, run validate and update-sql, then
   update. Apply schema before enabling receipt/treatment/result writers. Review
   the composite indexes on command_idempotency/event_outbox for deployment lock
   time on a production-sized clone; local fixtures do not establish that cost.
3. If any old 026–030 ran, retain their original contents/checksums. This replacement
   patch must NOT be deployed as-is. Create a forward-numbered migration after
   inspecting actual rows. Backfill source links only from retained command/event
   and receipt evidence; never invent identities or reinterpret unknown mass as
   zero. Stop for rows whose provenance cannot be recovered. Validate counts,
   totals, FK orphan checks and exact replay identity before enabling writers.
4. MySQL DDL can partially succeed. If a migration fails, stop writers, retain the
   error and inventory (including helper indexes). Repair the exact partial DDL on
   a clone first. Pre-write empty test tables may be removed in reverse dependency
   order; populated tables require a reviewed forward repair. Re-run validate and
   update, followed by persistence checks. Do not use clearCheckSums as a repair.
5. After business writes, roll back application deployment only if it can read the
   new schema safely. Never use the DROP TABLE rollback to undo accepted history.

The supplied backend receipt path has additional service/event concerns identified
in its separate review; these storage/telemetry corrections do not certify that
entire API implementation. Treatment/result services must implement the contract
above in their owning tasks before those endpoints are enabled.
