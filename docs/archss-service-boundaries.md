# ArchSS service boundaries and local integration

This records the branch implementation for AS-MS-I-J-01, AS-MS-I-J-02 and
AS-PW-I-J-01. It provides the route and table map needed to connect the four Go
processes. It does not establish an AD2 approval, cloud deployment or a completed
six-service lifecycle smoke.

## Build and configuration

From `src/backend`, run `make build-services` to compile the four entrypoints or
`make docker-build-services` to build their four images. The shared Dockerfile
selects its entrypoint through `--build-arg SERVICE=identity-access` (or
`batch-workflow`, `claim-logistics`, `custody-verification`). Each process exposes
`/healthz` and `/readyz` and uses the existing database, Redis and authentication
configuration. Use a separate `EWASTE_SERVER_PORT` and log file when running
multiple binaries on one host. Only batch-workflow starts the outbox publisher;
configure matching and analytics worker API origins to that service.

Set all four frontend variables before `next dev` or `next build` to enable the
split gateway: `IDENTITY_ACCESS_URL`, `BATCH_WORKFLOW_URL`, `CLAIM_LOGISTICS_URL`
and `CUSTODY_VERIFICATION_URL`. They are server destinations without path
suffixes. The frontend Dockerfile accepts them as build arguments. Rebuild the
frontend when changing these destinations. Leaving all unset preserves the
existing monolith proxy; partial split configuration fails early.

## Route ownership

The public contract remains `/api/v1`. Next.js rewrites specific commands before
the general batch and assignment routes. Service authentication is retained on
both analytics path variants; the worker can continue using its existing paths.

| Public path after `/api/v1`                          | Service              | Local path                                                                                      |
| ---------------------------------------------------- | -------------------- | ----------------------------------------------------------------------------------------------- |
| `/auth/*`                                            | identity-access      | `/v1/auth/*`                                                                                    |
| `/batches`, `/batches/:id`, `/batches/:id/submit`    | batch-workflow       | `/v1/batches…`                                                                                  |
| `/opportunities…`                                    | batch-workflow       | `/v1/opportunities…`                                                                            |
| `/batches/:id/claim`                                 | claim-logistics      | `/v1/claims/batches/:id`                                                                        |
| `/batches/:id/assignments`                           | claim-logistics      | `/v1/logistics/assignments/batches/:id`                                                         |
| `/assignments…`, including accept, reject and fail   | claim-logistics      | `/v1/logistics/assignments…`                                                                    |
| `/assignments/:id/handoff`                           | custody-verification | `/v1/custody/assignments/:id/handoff`                                                           |
| `/batches/:id/receipt`, `/batches/:id/treatment`     | custody-verification | `/v1/processing/batches/:id/receipt` or `/treatment`                                            |
| `/processing/batches…`                               | custody-verification | `/v1/processing/batches…`                                                                       |
| `/batches/:id/evidence…`                             | custody-verification | `/v1/evidence/batches/:id…`                                                                     |
| `/audit…`                                            | custody-verification | `/v1/audit…`                                                                                    |
| `/batches/:id/analytics-input`, `/analytics-results` | batch-workflow       | `/internal/v1/analytics/batches/:id/input` or `/results`; original paths also accepted directly |

Matching uses the existing service-authenticated `/internal/v1/matching/runs…`
routes directly on batch-workflow when matching is enabled.

## Logical table ownership

Each table has one logical owner below. The four processes still share a MySQL
schema and in-module repository implementations; this is not database-enforced
write isolation. Migrations and approved seed/configuration writes remain shared
deployment responsibilities.

| Logical owner        | Tables                                                                                                                                                                                                                                                                            |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| identity-access      | `organisations`, `roles`, `users`, `sessions`, `login_audit`                                                                                                                                                                                                                      |
| batch-workflow       | `ewaste_batches`, `command_idempotency`, `batch_audit_events`, `event_outbox`, `matching_rule_sets`, `recycler_matching_profiles`, `recycler_category_capabilities`, `recycler_service_zones`, `matching_decisions`, `matched_results`, `batch_impact_metrics`, `batch_anomalies` |
| claim-logistics      | `batch_claims`, `capacity_reservations`, `recycler_capacity_pools`, `recycler_collector_scopes`, `batch_assignments`, `assignment_actions`                                                                                                                                        |
| custody-verification | `batch_handoffs`, `batch_receipts`, `batch_treatments`, `batch_evidence`                                                                                                                                                                                                          |

Cross-context writes use the shared transaction implementation: a claim commits
capacity, claim, assignment, batch state, command, audit and outbox together.
Custody commands also update assignment/batch state and append audit/outbox rows.
Failed pickup stays in claim-logistics because it replaces the collector in the
same transaction, while using the custody handoff repository. This is the
implementation's refinement of the plan's broader custody responsibility and
must be reconciled in the team's AD2 record. Other services read identity/session
tables to preserve the existing authentication checks. Python workers submit
authoritative results through Go APIs.

## Collector and conflict contract

A successful automatic claim returns `ASSIGNED` and a `PENDING` assignment.
The collector accepts that assignment before handoff or failed pickup. Rejection
and failure assign the next eligible collector when one exists. Assignment reads
include `batch_version` for `If-Match-Version`; `version` remains the separate
assignment version. The read projection requires no schema migration.

Stale commands return HTTP 409 `STALE_VERSION` with `current_state`, containing
assignment and batch IDs, statuses and versions plus the current assignment ID
when present. The client displays that snapshot and does not resend automatically.
Assignment commands lock the batch before requested/current assignment rows.

## Remaining delivery evidence

Per-service CI/CD, isolated Terraform apps, ingress restrictions and deployment
digests remain the AS-CN implementation tasks. Before claiming integrated Done,
retain real MySQL concurrency/rollback evidence and the lifecycle smoke through
one entry with both workers, then record image digests, replica settings and the
team's ownership approval. Source and unit tests alone do not establish those
results.
