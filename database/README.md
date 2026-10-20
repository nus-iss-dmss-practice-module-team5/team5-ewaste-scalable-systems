# EW-101-C1 Persistence Foundation

The Sprint 1 instructions below describe the existing foundation. The master
changelog now also includes approved Sprint 2 C1 migrations 006–010, C2
prerequisites 011–017, C3 claim migrations 018–020, and C4 assignment/handoff migrations 021–025. For
EWCSB-126 deterministic batch fixtures and SQL persistence/migration checks,
see [the C1 test guide](tests/c1/README.md) and run:

```sh
./scripts/test-ewcsb126.sh
```

C1 batch fixtures require the explicit `c1-fixtures` context; the existing
`seed` context loads synthetic Sprint 1 identities and the `binary-v1` matching
policy from `seed/105-seed-matching-rule-sets.sql`. The policy seed runs once in
dev/staging, references the active platform administrator `USR-001`, and becomes
effective at migration time in UTC on an empty installation. An identical,
active `binary-v1` policy with a valid UUIDv4 is retained with its original ID,
creator and timestamps. The known manual seed ID
`r2260000-0000-4000-8000-000000000001` is corrected to
`a1290000-0000-4000-8000-000000000001` only if it has no references in matching
decisions, frozen command responses, audit records or outbox events. Its policy
and timestamps are preserved. Other invalid IDs, different policy content,
future/retired policies, ID collisions, overlapping activation windows, and a
missing/inactive administrator halt the migration without changing data.

Repeated Liquibase updates leave the policy and its activation timestamp
unchanged. Seed 105 accepts its original checksum explicitly so databases where
it already succeeded retain their changelog entry without rerunning the seed.

The policy JSON matches `contracts/matching/contracts/MatchingInput.v1.schema.json`.
Recycler profiles, capabilities, capacity pools and service zones still require
configuration before a receiver can qualify for a match. This seed creates no
batches, matching results or recycler configuration. Production excludes the
`seed` context and must provision the policy with its own approved administrator
and activation time. Policies referenced by matching decisions are retained;
this seed has no destructive rollback.

For the reported dev configuration IDs (`p226...`, `c1`–`c8`, `z1`–`z10`), use
the [migration and deployment recovery guide](maintenance/README.md).
Changeset `EWCSB129-106` runs after seed 105 in the master changelog with the
explicit `seed` context. It preserves business values and rejects historical
references or reserved capacity. Databases without these old IDs are unchanged;
Liquibase records a successful no-op. No manual SQL repair is required.

Changeset `EWCSB4-107` adds the confirmed dev/staging collection scopes with the
explicit `seed` context:

| Collector | Recycler holding the accepted claim | Batch zone |
| --- | --- | --- |
| COL-001 | PROC-001 | NORTH |
| COL-001 | PROC-002 | EAST |
| COL-002 | PROC-001 | NORTH |

The one backup scope uses the existing `collector2@ewaste.test` account
(`USR-006`). It is necessary to test concurrent assignment selection and
replacement after rejection: the same collector cannot immediately select
their previous assignment again. Run those cases with a NORTH batch claimed by
PROC-001. EAST remains assigned only to COL-001 by this seed. No new accounts,
organisations or business records are required.

New scopes are active from migration time, have no expiry and start at version
1. Existing valid scopes retain their IDs, versions and dates. Missing/inactive
or incorrectly typed organisations, conflicting IDs, and inactive, expired,
future-dated or invalid-ID scopes halt the migration for review. Other scope
rows are retained. Scope identities must remain available for assignment history,
so this seed has no destructive rollback and is excluded from production.

These scopes control collector visibility and assignment. They do not change
which recyclers qualify for matching opportunities: recycler matching profiles,
capabilities, capacity and service zones still determine that eligibility.
The collector list requires an `APPROVED` batch with a current accepted claim
whose recycler and zone match an active scope. After migration, rerun the
collector lifecycle test to verify the deployed API behavior.

Changeset `EWCSB129-108` fills missing matching configuration for `PROC-001` and
`PROC-002` with the explicit `seed` context in dev/staging:

- One active profile and one shared `MAIN` pool of **50,000.00 kg per recycler**.
- All four categories accept `FUNCTIONAL`, `REPAIRABLE` and `END_OF_LIFE` items.
- Data-bearing items are supported for `ICT_EQUIPMENT` and `CONSUMER_ELECTRONICS`.
- All five zones are active with zero minimum lead minutes. The collection
  deadline must still be in the future when matching evaluates the batch.

New rows have deterministic UUIDv4 IDs, version 1 and UTC migration timestamps;
new pools start with zero reserved kg. Compatible existing rows keep their IDs,
versions, dates and reservations. Conflicting settings, disabled profiles or
configuration, invalid IDs, ID collisions and invalid recycler organisations
halt before any seed rows are inserted. Collector scopes remain unchanged.
Seed 106 repairs the old reported IDs first; 108 also supports a fresh database.
Production excludes `seed` and requires its own approved configuration.

Apply the normal dev/staging migration, then rerun the API/JMeter workflow with
a new batch. Existing `NO_MATCH` decisions retain their frozen inputs; waiting
longer or replaying the same event does not replace them. Do not
seed batches, claims, assignments, handoffs, audit events or outbox messages to
bypass the APIs: the test actions should create those records.

Seed data cannot fill implementation gaps. In this branch, assignment selection
creates an `ACCEPTED` assignment, so a successful separate acceptance requires
a legacy `PENDING` assignment. Do not seed one just to make that endpoint pass.
Failed pickup persists `FAILED_COLLECTION`; `RecoverFailedCollection` exists
as a service method but has no caller, so automatic recovery/reassignment after
failure cannot yet be verified end to end. Reassignment after rejection can be
tested through the exposed APIs with the backup collector.

For C3 claim repository, constraints and concurrent MySQL fixtures, see
[the C3 test guide](tests/c3/README.md) and run:

```sh
./scripts/test-c3-persistence.sh
```

For C4 assignment/handoff persistence, canonical outbox events and lifecycle
concurrency checks, see [the C4 test guide](tests/c4/README.md) and run:

```sh
./scripts/test-c4-persistence.sh
```

The sections below describe the original Sprint 1 foundation. Its scope exclusions
are historical; the main changelog now includes the later C1–C4 business tables.

This folder implements the Sprint 1 database foundation for:

- organisations and organisation ownership;
- the Sprint 1 role catalogue;
- seeded synthetic users;
- short-lived, revocable login sessions; and
- repeatable Liquibase migrations and database assertions.

## Scope boundary

This change deliberately does **not** create:

- the append-only audit-event table, which belongs to EW-102-C2;
- endpoint handlers or password verification code, which belong to EW-101-C2;
- the detailed permission matrix or RBAC middleware, which belong to EW-102; or
- business tables for e-waste batches, claims, collection, processing, or recycling.

Keeping these concerns separate makes the EW-101-C1 pull request small and reviewable.

## Current organisation hierarchy

```text
PLATFORM
|- SYSTEM_ADMIN
`- AUDITOR

DONOR organisations
`- DONOR

COLLECTION_OPERATOR organisations
`- COLLECTOR

PROCESSING_FACILITY organisations
`- RECYCLER
```

The `roles.allowed_organisation_type` column documents this ownership rule. The Go service should enforce it when users are created or updated. The included database test also verifies that the synthetic seed data follows the rule.

## Seeded synthetic accounts

| Email | Role | Organisation | Status |
|---|---|---|---|
| admin@ewaste.test | SYSTEM_ADMIN | PLATFORM | ACTIVE |
| auditor@ewaste.test | AUDITOR | PLATFORM | ACTIVE |
| donor1@ewaste.test | DONOR | DON-001 | ACTIVE |
| donor2@ewaste.test | DONOR | DON-002 | ACTIVE |
| collector1@ewaste.test | COLLECTOR | COL-001 | ACTIVE |
| collector2@ewaste.test | COLLECTOR | COL-002 | ACTIVE |
| recycler1@ewaste.test | RECYCLER | PROC-001 | ACTIVE |
| recycler2@ewaste.test | RECYCLER | PROC-002 | ACTIVE |
| disabled@ewaste.test | DONOR | DON-001 | DISABLED |

For the local Sprint 1 smoke test, all accounts use the synthetic password:

```text
TestOnly#2026!
```

Only bcrypt hashes are stored in `seed/103-seed-users.sql`. These accounts are test fixtures. The seed changesets use the required `seed` context and must not be applied to a real production system.

## Session model assumption

The persistence starter now assumes JWT-based authentication with server-side session revocation:

- `sessions.session_id` is the UUID for the authenticated server-side session and should be carried in access and refresh JWTs as the `sid` claim.
- `sessions.token_hash` stores a 64-character SHA-256 hexadecimal hash of the **current refresh token/JWT**. The raw refresh JWT is never stored in MySQL.
- Access JWTs are short-lived and are not stored in MySQL.
- Each individual JWT may use its own `jti`; `jti` is distinct from the shared session `sid`.

A successful login can:

1. generate a new session UUID;
2. create the access JWT and refresh JWT;
3. hash the refresh JWT with SHA-256;
4. insert the session row with `session_id`, `user_id`, `token_hash`, issue/expiry timestamps, and no revocation timestamp; and
5. return the JWTs to the client without persisting either raw JWT.

On refresh, the presented refresh JWT is hashed and compared with `token_hash`. If refresh-token rotation is enabled, replace `token_hash` with the hash of the newly issued refresh JWT. Logout or forced invalidation sets `revoked_at`; raw JWTs must never be written to the database, logs, or audit payloads.

## Changelog order

```text
001 organisations
 -> 002 roles
 -> 003 users
 -> 004 sessions
 -> 101 seed organisations
 -> 102 seed roles
 -> 103 seed users
```

The order preserves the foreign-key dependencies.

## Run locally

From the repository root:

```bash
./scripts/test-ew101-c1.sh
```

The script:

1. starts a clean MySQL 8.4 container;
2. builds a pinned Liquibase image with the MySQL driver;
3. validates the changelog;
4. applies the schema and explicit `seed` context;
5. runs the migration a second time to check repeatability; and
6. runs database assertions.

Keep the database running after the test:

```bash
KEEP_DB=1 ./scripts/test-ew101-c1.sh
```

Default local connection:

```text
Host:     localhost
Port:     3307
Database: ewaste
User:     ewaste_app
Password: ewaste-local-only
```

These defaults are for isolated local development only. Override them through environment variables when needed:

```bash
export EWASTE_DB_NAME=ewaste
export EWASTE_DB_USER=ewaste_app
export EWASTE_DB_PASSWORD='a-local-password'
export EWASTE_DB_ROOT_PASSWORD='a-local-root-password'
export EWASTE_DB_PORT=3307
```

## Suggested pull request evidence

Attach or link:

- Liquibase `validate` output;
- first successful `update` output;
- second no-op `update` output;
- the PASS table from `verify-ew101-c1.sql`;
- a schema/ER screenshot if required by the sprint; and
- confirmation that only password hashes, not raw passwords or session tokens, are stored.
