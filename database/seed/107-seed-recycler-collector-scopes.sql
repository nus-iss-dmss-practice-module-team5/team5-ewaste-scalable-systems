--liquibase formatted sql

--changeset team5:EWCSB4-107 dbms:mysql context:@seed labels:synthetic-test-data,collector-configuration
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:2 SELECT COUNT(*) FROM organisations WHERE organisation_id IN ('COL-001', 'COL-002') AND organisation_type = 'COLLECTION_OPERATOR' AND status = 'ACTIVE';
--precondition-sql-check expectedResult:2 SELECT COUNT(*) FROM organisations WHERE organisation_id IN ('PROC-001', 'PROC-002') AND organisation_type = 'PROCESSING_FACILITY' AND status = 'ACTIVE';
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_collector_scopes WHERE ((collector_org_id IN ('COL-001', 'COL-002') AND recycler_org_id = 'PROC-001' AND zone = 'NORTH') OR (collector_org_id = 'COL-001' AND recycler_org_id = 'PROC-002' AND zone = 'EAST')) AND (is_active <> TRUE OR valid_from > UTC_TIMESTAMP(6) OR (valid_until IS NOT NULL AND valid_until <= UTC_TIMESTAMP(6)) OR NOT REGEXP_LIKE(id, '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$', 'c'));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_collector_scopes WHERE (id = 'e1070000-0000-4000-8000-000000000001' AND NOT (recycler_org_id = 'PROC-001' AND collector_org_id = 'COL-001' AND zone = 'NORTH')) OR (id = 'e1070000-0000-4000-8000-000000000002' AND NOT (recycler_org_id = 'PROC-002' AND collector_org_id = 'COL-001' AND zone = 'EAST')) OR (id = 'e1070000-0000-4000-8000-000000000003' AND NOT (recycler_org_id = 'PROC-001' AND collector_org_id = 'COL-002' AND zone = 'NORTH'));
--comment: Seed the dev/staging collector scopes and one backup collector for reassignment tests.
-- Each missing scope activates at migration time with no expiry. Preserve a
-- valid existing scope's identity, version and dates, including any expiry.
-- COL-002/PROC-001/NORTH is the one extra scope needed for concurrent selection
-- and replacement after rejection, using the existing USR-006 account.
-- Do not reactivate disabled scopes or change recycler matching eligibility.
INSERT INTO recycler_collector_scopes (
    id, recycler_org_id, collector_org_id, zone, is_active, version,
    valid_from, valid_until, created_at, updated_at
)
SELECT
    seed.id, seed.recycler_org_id, seed.collector_org_id, seed.zone, TRUE, 1,
    UTC_TIMESTAMP(6), NULL, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)
FROM (
    SELECT 'e1070000-0000-4000-8000-000000000001' AS id,
           'PROC-001' AS recycler_org_id, 'COL-001' AS collector_org_id, 'NORTH' AS zone
    UNION ALL
    SELECT 'e1070000-0000-4000-8000-000000000002', 'PROC-002', 'COL-001', 'EAST'
    UNION ALL
    SELECT 'e1070000-0000-4000-8000-000000000003', 'PROC-001', 'COL-002', 'NORTH'
) AS seed
WHERE NOT EXISTS (
    SELECT 1 FROM recycler_collector_scopes existing
    WHERE existing.recycler_org_id = seed.recycler_org_id
      AND existing.collector_org_id = seed.collector_org_id
      AND existing.zone = seed.zone
);

-- No destructive rollback: assignments can reference these scope identities.
-- Production requires explicitly approved collector relationships of its own.
