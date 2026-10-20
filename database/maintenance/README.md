# Recover the reported dev matcher configuration

Changeset `EWCSB129-106` in
[`106-repair-matching-config-ids.sql`](../seed/106-repair-matching-config-ids.sql)
addresses the September 26 dev incident: two `p226...` pool IDs, eight capability
IDs `c1`–`c8`, and ten zone IDs `z1`–`z10` violate the approved UUIDv4 contract.
It runs through the master Liquibase changelog after seed 105, using the explicit
`seed` context already used for dev/staging. Production excludes that context.

The migration maps those IDs to UUIDv4 values starting with `b2260000`,
`c2260000`, and `d2260000`, respectively. It creates no new recycler business
configuration and preserves owners, categories, conditions, weights, active
flags, zones and lead times. Changed configuration rows advance their versions
once and update their timestamps; profiles, batches and historical records
remain unchanged.

The repair locks the relevant rows and updates pool relationships in one
transaction with foreign-key checks enabled. It halts if the reported rows are
incomplete or differ, replacement IDs collide, capacity is reserved, or old IDs
appear in decisions, results, reservations, prepared commands, outbox payloads
or audit details. Any data error rolls back every repair change. If none of the
old IDs exist, including on fresh or already repaired databases, it is a no-op.
It does not attempt to repair unrelated invalid configuration.

MySQL routine DDL commits implicitly, so this changeset uses
`runInTransaction:false`; the helper procedure manages the data transaction and
rolls it back on errors. Liquibase records completion after the procedure and
cleanup succeed. A retry is safe even if the data committed before that record
was written. A failed attempt may leave the helper procedure; the next attempt
replaces it. No automatic rollback restores invalid IDs that later history
could reference.

## 1. Apply the database migration

Commit and merge the changes through the normal PR process. The push to dev
triggers the existing **Database - Liquibase Migration** workflow automatically.
For a manual rerun,
select `target_env: dev` and `command: update`. That workflow already supplies
the `seed` context. Do not run the earlier manual Bash repair
or clear Liquibase checksums.

Require a successful workflow and verify the recorded changeset:

```sql
SELECT ID, AUTHOR, FILENAME, EXECTYPE, DATEEXECUTED
FROM DATABASECHANGELOG
WHERE ID = 'EWCSB129-106' AND AUTHOR = 'team5';
```

Expect one `EXECUTED` entry. On an unaffected database this records a successful
no-op. If a guard halts the migration, retain the error for review instead of
deleting history or disabling constraints. No failed changeset is marked as
applied. The migration identity needs permission to create/drop routines and
create temporary tables as well as its existing data privileges.

## 2. Apply the infrastructure configuration

The infrastructure repair is already included in code:

- `terraform/environments/main.tf` retains the API matching facade, Kafka
  publisher configuration, worker startup settings and shared signing-key
  references on every apply.
- `terraform/environments/variables.tf` declares and validates the sensitive
  matcher signing key.
- `.github/workflows/terraform-infra-create.yml` passes the environment's
  existing `MATCHER_SIGNING_SECRET` to Terraform and checks the configuration
  before applying it.

Keep the existing `MATCHER_SIGNING_SECRET` in the dev GitHub environment. Both
IaC and CD use that same secret; do not put its value in repository files or
generate a new key just for this recovery.

Merging these Terraform changes to dev triggers **IaC - Create Cloud Resources
(Dev, Stg, Prod)**. For a manual rerun, select `target_env: dev` and
`action: apply`. The Terraform changes preserve the existing image deployment,
ingress, identity and scaling conventions.

## 3. Deploy and restart through CD

The existing **CD - Multi-Environment Deployment & Evidence Pipeline** calls
`scripts/deploy-matcher.sh` from its analytics deployment step. That code
restores both applications' settings and secret references, deploys the immutable
worker image, restarts revisions, verifies authenticated API access and checks
worker readiness. No separate Cloud Shell command or portal edit is required.

Database migration, IaC and CD are independent workflows and may start together
on the same merge. For this recovery, require migration 106 and the corrected
IaC apply to succeed, then run CD for dev if it ran before those prerequisites
completed or failed. Use `target_env: dev` and leave `rollback_tag` empty.
If the signing key is rotated later, run CD after IaC so both applications
reload it.

The deployment preserves `matching-worker-v1` and existing Kafka offsets.
Pending delivery retries automatically; do not recreate the batch or publish a
replacement event. Keep the corrected Terraform definitions in the branch so a
later infrastructure apply retains the matcher settings.

## Verification

`bash scripts/test-persistence.sh` includes the repair fixtures and rejection,
rollback, foreign-key, version and repeat-run checks in its existing standalone
SQL suite. No Go code or earlier per-task test files are needed. The tests
execute the repair through Liquibase and verify its changelog entry,
no-op behavior on unaffected databases, and adoption of already repaired data.

The IaC workflow runs `terraform validate` and the mocked-provider tests in
`terraform/environments/tests/matcher.tftest.hcl` before applying infrastructure.
Those tests require Terraform 1.8 and never contact Azure.

After recovery, check `/readyz` and the worker's console logs. The expected
delivery reaches `offset_committed` rather than `delivery_paused`. Query
`matching_decisions` for the source trigger
`c64d1dba-ea46-4ed3-a73e-438d2c53dc92` and inspect its `matched_results` to confirm
the actual persisted outcome. Matching uses the recovery-time snapshot; an
expired collection deadline correctly produces `NO_MATCH`.
