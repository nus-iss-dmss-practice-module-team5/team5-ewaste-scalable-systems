--liquibase formatted sql

--changeset team5:EWCSB129-108 dbms:mysql context:@seed labels:synthetic-test-data,matching-configuration
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:2 SELECT COUNT(*) FROM organisations WHERE organisation_id IN ('PROC-001', 'PROC-002') AND organisation_type = 'PROCESSING_FACILITY' AND status = 'ACTIVE';
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_matching_profiles WHERE recycler_org_id IN ('PROC-001', 'PROC-002') AND is_active <> TRUE;
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_capacity_pools WHERE recycler_org_id IN ('PROC-001', 'PROC-002') AND pool_code = 'MAIN' AND (is_active <> TRUE OR total_kg <> 50000.00 OR NOT REGEXP_LIKE(id, '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$', 'c'));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_category_capabilities c JOIN recycler_capacity_pools p ON p.id = c.capacity_pool_id AND p.recycler_org_id = c.recycler_org_id WHERE c.recycler_org_id IN ('PROC-001', 'PROC-002') AND (c.is_active <> TRUE OR p.pool_code <> 'MAIN' OR c.supports_data_bearing <> (c.category IN ('ICT_EQUIPMENT', 'CONSUMER_ELECTRONICS')) OR JSON_LENGTH(c.accepted_conditions_json) <> 3 OR NOT JSON_CONTAINS(c.accepted_conditions_json, '["FUNCTIONAL","REPAIRABLE","END_OF_LIFE"]') OR NOT REGEXP_LIKE(c.id, '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$', 'c'));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_service_zones WHERE recycler_org_id IN ('PROC-001', 'PROC-002') AND (is_active <> TRUE OR minimum_lead_minutes <> 0 OR NOT REGEXP_LIKE(id, '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$', 'c'));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_capacity_pools WHERE id IN ('b1080000-0000-4000-8000-000000000001', 'b1080000-0000-4000-8000-000000000002') AND (pool_code <> 'MAIN' OR recycler_org_id <> CONCAT('PROC-00', RIGHT(id, 1)));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_category_capabilities WHERE id IN ('c1080000-0000-4000-8000-000000000001', 'c1080000-0000-4000-8000-000000000002', 'c1080000-0000-4000-8000-000000000003', 'c1080000-0000-4000-8000-000000000004', 'c1080000-0000-4000-8000-000000000005', 'c1080000-0000-4000-8000-000000000006', 'c1080000-0000-4000-8000-000000000007', 'c1080000-0000-4000-8000-000000000008') AND (recycler_org_id <> CONCAT('PROC-00', IF(RIGHT(id, 1) <= '4', 1, 2)) OR category <> ELT(MOD(CAST(RIGHT(id, 1) AS UNSIGNED) - 1, 4) + 1, 'ICT_EQUIPMENT', 'LARGE_APPLIANCE', 'BATTERIES', 'CONSUMER_ELECTRONICS'));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM recycler_service_zones WHERE id IN ('d1080000-0000-4000-8000-000000000001', 'd1080000-0000-4000-8000-000000000002', 'd1080000-0000-4000-8000-000000000003', 'd1080000-0000-4000-8000-000000000004', 'd1080000-0000-4000-8000-000000000005', 'd1080000-0000-4000-8000-000000000006', 'd1080000-0000-4000-8000-000000000007', 'd1080000-0000-4000-8000-000000000008', 'd1080000-0000-4000-8000-000000000009', 'd1080000-0000-4000-8000-000000000010') AND (recycler_org_id <> CONCAT('PROC-00', IF(CAST(RIGHT(id, 2) AS UNSIGNED) <= 5, 1, 2)) OR zone <> ELT(MOD(CAST(RIGHT(id, 2) AS UNSIGNED) - 1, 5) + 1, 'NORTH', 'SOUTH', 'EAST', 'WEST', 'CENTRAL'));
--comment: Fill the approved dev/staging matching configuration for both synthetic recyclers.
-- Insert only missing natural keys. Compatible existing rows retain their IDs,
-- versions, timestamps and reserved capacity; conflicting settings halt above.
-- MAIN is shared by all four categories, not 50,000 kg per category.
-- No business records or collector permissions are created or changed.

INSERT INTO recycler_matching_profiles (
    recycler_org_id, is_active, version, created_at, updated_at
)
SELECT o.organisation_id, TRUE, 1, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)
FROM organisations o
WHERE o.organisation_id IN ('PROC-001', 'PROC-002')
  AND NOT EXISTS (
      SELECT 1 FROM recycler_matching_profiles p
      WHERE p.recycler_org_id = o.organisation_id
  );

INSERT INTO recycler_capacity_pools (
    id, recycler_org_id, pool_code, total_kg, reserved_kg, is_active, version, updated_at
)
SELECT CONCAT('b1080000-0000-4000-8000-00000000000', RIGHT(o.organisation_id, 1)),
       o.organisation_id, 'MAIN', 50000.00, 0.00, TRUE, 1, UTC_TIMESTAMP(6)
FROM organisations o
WHERE o.organisation_id IN ('PROC-001', 'PROC-002')
  AND NOT EXISTS (
      SELECT 1 FROM recycler_capacity_pools p
      WHERE p.recycler_org_id = o.organisation_id AND p.pool_code = 'MAIN'
  );

INSERT INTO recycler_category_capabilities (
    id, recycler_org_id, category, accepted_conditions_json, supports_data_bearing,
    is_active, capacity_pool_id, version, updated_at
)
SELECT CONCAT('c1080000-0000-4000-8000-',
              LPAD((CAST(RIGHT(p.recycler_org_id, 1) AS UNSIGNED) - 1) * 4 + c.n, 12, '0')),
       p.recycler_org_id, c.category, '["FUNCTIONAL","REPAIRABLE","END_OF_LIFE"]',
       c.data_bearing, TRUE, p.id, 1, UTC_TIMESTAMP(6)
FROM recycler_capacity_pools p
CROSS JOIN (
    SELECT 1 AS n, 'ICT_EQUIPMENT' AS category, TRUE AS data_bearing
    UNION ALL SELECT 2, 'LARGE_APPLIANCE', FALSE
    UNION ALL SELECT 3, 'BATTERIES', FALSE
    UNION ALL SELECT 4, 'CONSUMER_ELECTRONICS', TRUE
) c
WHERE p.recycler_org_id IN ('PROC-001', 'PROC-002') AND p.pool_code = 'MAIN'
  AND NOT EXISTS (
      SELECT 1 FROM recycler_category_capabilities existing
      WHERE existing.recycler_org_id = p.recycler_org_id AND existing.category = c.category
  );

INSERT INTO recycler_service_zones (
    id, recycler_org_id, zone, minimum_lead_minutes, is_active, version, updated_at
)
SELECT CONCAT('d1080000-0000-4000-8000-',
              LPAD((CAST(RIGHT(p.recycler_org_id, 1) AS UNSIGNED) - 1) * 5 + z.n, 12, '0')),
       p.recycler_org_id, z.zone, 0, TRUE, 1, UTC_TIMESTAMP(6)
FROM recycler_matching_profiles p
CROSS JOIN (
    SELECT 1 AS n, 'NORTH' AS zone
    UNION ALL SELECT 2, 'SOUTH'
    UNION ALL SELECT 3, 'EAST'
    UNION ALL SELECT 4, 'WEST'
    UNION ALL SELECT 5, 'CENTRAL'
) z
WHERE p.recycler_org_id IN ('PROC-001', 'PROC-002')
  AND NOT EXISTS (
      SELECT 1 FROM recycler_service_zones existing
      WHERE existing.recycler_org_id = p.recycler_org_id AND existing.zone = z.zone
  );

-- No destructive rollback: matching history and claims may reference these IDs.
-- Production excludes @seed and supplies its own approved configuration.
