--liquibase formatted sql

--changeset team5:EWCSB129-106 dbms:mysql context:@seed labels:matching-configuration endDelimiter:// runInTransaction:false
--comment: Repair the reported dev recycler configuration IDs without rewriting historical records.
-- Only the known manual IDs are eligible; unaffected and repaired databases are no-ops.
-- Routine DDL commits implicitly in MySQL. The CALL owns the atomic data transaction
-- and rolls it back on any error. A retry after commit is safe even if Liquibase
-- did not yet record completion. No foreign-key checks are disabled.

DROP PROCEDURE IF EXISTS repair_ewcsb129_config_ids//
CREATE PROCEDURE repair_ewcsb129_config_ids()
repair_block: BEGIN
    DECLARE source_count INT DEFAULT 0;
    DECLARE target_count INT DEFAULT 0;
    DECLARE locked_count BIGINT DEFAULT 0;
    DECLARE repaired_at DATETIME(6);
    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        ROLLBACK;
        RESIGNAL;
    END;

    IF @@foreign_key_checks <> 1 THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Repair requires foreign_key_checks=1';
    END IF;

    CREATE TEMPORARY TABLE repair_pools (
        old_id VARCHAR(36) PRIMARY KEY,
        new_id VARCHAR(36) NOT NULL UNIQUE,
        recycler_org_id VARCHAR(32) NOT NULL,
        natural_key VARCHAR(64) NOT NULL
    ) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
    CREATE TEMPORARY TABLE repair_capabilities LIKE repair_pools;
    CREATE TEMPORARY TABLE repair_zones LIKE repair_pools;
    INSERT INTO repair_pools VALUES
        ('p2260000-0000-4000-8000-000000000001','b2260000-0000-4000-8000-000000000001','PROC-001','MAIN'),
        ('p2260000-0000-4000-8000-000000000002','b2260000-0000-4000-8000-000000000002','PROC-002','MAIN');
    INSERT INTO repair_capabilities VALUES
        ('c1','c2260000-0000-4000-8000-000000000001','PROC-001','ICT_EQUIPMENT'),
        ('c2','c2260000-0000-4000-8000-000000000002','PROC-001','LARGE_APPLIANCE'),
        ('c3','c2260000-0000-4000-8000-000000000003','PROC-001','BATTERIES'),
        ('c4','c2260000-0000-4000-8000-000000000004','PROC-001','CONSUMER_ELECTRONICS'),
        ('c5','c2260000-0000-4000-8000-000000000005','PROC-002','ICT_EQUIPMENT'),
        ('c6','c2260000-0000-4000-8000-000000000006','PROC-002','LARGE_APPLIANCE'),
        ('c7','c2260000-0000-4000-8000-000000000007','PROC-002','BATTERIES'),
        ('c8','c2260000-0000-4000-8000-000000000008','PROC-002','CONSUMER_ELECTRONICS');
    INSERT INTO repair_zones VALUES
        ('z1','d2260000-0000-4000-8000-000000000001','PROC-001','NORTH'),
        ('z2','d2260000-0000-4000-8000-000000000002','PROC-001','SOUTH'),
        ('z3','d2260000-0000-4000-8000-000000000003','PROC-001','EAST'),
        ('z4','d2260000-0000-4000-8000-000000000004','PROC-001','WEST'),
        ('z5','d2260000-0000-4000-8000-000000000005','PROC-001','CENTRAL'),
        ('z6','d2260000-0000-4000-8000-000000000006','PROC-002','NORTH'),
        ('z7','d2260000-0000-4000-8000-000000000007','PROC-002','SOUTH'),
        ('z8','d2260000-0000-4000-8000-000000000008','PROC-002','EAST'),
        ('z9','d2260000-0000-4000-8000-000000000009','PROC-002','WEST'),
        ('z10','d2260000-0000-4000-8000-000000000010','PROC-002','CENTRAL');
    CREATE TEMPORARY TABLE repair_ids AS
        SELECT 'pool' AS kind, p.* FROM repair_pools p
        UNION ALL SELECT 'capability', c.* FROM repair_capabilities c
        UNION ALL SELECT 'zone', z.* FROM repair_zones z;
    CREATE TEMPORARY TABLE repair_pool_rows LIKE recycler_capacity_pools;

    SET TRANSACTION ISOLATION LEVEL SERIALIZABLE;
    START TRANSACTION;
    -- Keep the reference checks and the repair in one locked transaction.
    -- Match preparation also locks command/configuration rows. A deadlock or
    -- lock timeout rolls this repair back; rerun after the competing work ends.
    SELECT COUNT(*) INTO locked_count FROM command_idempotency FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM organisations FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM matching_rule_sets FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM recycler_matching_profiles FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM recycler_capacity_pools FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM recycler_category_capabilities FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM recycler_service_zones FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM matching_decisions FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM matched_results FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM capacity_reservations FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM event_outbox FOR UPDATE;
    SELECT COUNT(*) INTO locked_count FROM batch_audit_events FOR UPDATE;

    SELECT COUNT(*), COALESCE(SUM(x.kind = m.kind AND x.owner = m.recycler_org_id
                                 AND x.natural_key = m.natural_key), 0)
    INTO source_count, target_count
    FROM (
        SELECT 'pool' AS kind, id, recycler_org_id AS owner, pool_code AS natural_key FROM recycler_capacity_pools
        UNION ALL SELECT 'capability', id, recycler_org_id, category FROM recycler_category_capabilities
        UNION ALL SELECT 'zone', id, recycler_org_id, zone FROM recycler_service_zones
    ) x JOIN repair_ids m ON x.id = m.old_id;

    IF source_count = 0 THEN
        -- New installations and databases already repaired manually need no change.
        COMMIT;
        SELECT 'NO_REPAIR_NEEDED' AS repair_status;
        LEAVE repair_block;
    END IF;
    IF source_count <> 20 OR target_count <> 20 THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Reported IDs, owners or configuration keys differ; no changes made';
    END IF;
    IF EXISTS (
        SELECT 1 FROM (
            SELECT id FROM recycler_capacity_pools
            UNION ALL SELECT id FROM recycler_category_capabilities
            UNION ALL SELECT id FROM recycler_service_zones
        ) x JOIN repair_ids m ON x.id = m.new_id
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Replacement UUID already exists; no changes made';
    END IF;

    IF EXISTS (
        SELECT 1 FROM repair_ids m WHERE
            EXISTS (SELECT 1 FROM matching_decisions d WHERE JSON_SEARCH(d.input_snapshot_json, 'one', m.old_id) IS NOT NULL)
            OR EXISTS (SELECT 1 FROM matched_results r WHERE r.capacity_pool_id = m.old_id OR JSON_SEARCH(r.evidence_json, 'one', m.old_id) IS NOT NULL)
            OR EXISTS (SELECT 1 FROM capacity_reservations r WHERE r.capacity_pool_id = m.old_id)
            OR EXISTS (SELECT 1 FROM command_idempotency c WHERE JSON_SEARCH(c.response_json, 'one', m.old_id) IS NOT NULL)
            OR EXISTS (SELECT 1 FROM event_outbox e WHERE JSON_SEARCH(e.payload_json, 'one', m.old_id) IS NOT NULL)
            OR EXISTS (SELECT 1 FROM batch_audit_events a WHERE JSON_SEARCH(a.details_json, 'one', m.old_id) IS NOT NULL)
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Historical or prepared records reference the old IDs; review required, no changes made';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recycler_capacity_pools p JOIN repair_pools m ON p.id = m.old_id
        WHERE p.reserved_kg <> 0 OR p.version = 9223372036854775807
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Reserved capacity or pool version overflow; no changes made';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recycler_category_capabilities c JOIN repair_capabilities m ON c.id = m.old_id
        LEFT JOIN repair_pools p ON c.capacity_pool_id = p.old_id AND c.recycler_org_id = p.recycler_org_id
        WHERE p.old_id IS NULL OR c.version = 9223372036854775807
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Unexpected capability pool link or version overflow; no changes made';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recycler_service_zones z JOIN repair_zones m ON z.id = m.old_id
        WHERE z.version = 9223372036854775807
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Service-zone version overflow; no changes made';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recycler_capacity_pools p JOIN repair_pools m
        ON p.recycler_org_id = m.recycler_org_id AND p.pool_code = CONCAT('__repair__', m.new_id)
    ) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Temporary pool-name collision; no changes made';
    END IF;

    SET repaired_at = UTC_TIMESTAMP(6);
    INSERT INTO repair_pool_rows SELECT p.* FROM recycler_capacity_pools p JOIN repair_pools m ON p.id = m.old_id;
    -- Free each unique owner/pool-code pair before creating its replacement.
    -- Readers cannot observe these temporary names outside this transaction.
    UPDATE recycler_capacity_pools p JOIN repair_pools m ON p.id = m.old_id
    SET p.pool_code = CONCAT('__repair__', m.new_id);
    INSERT INTO recycler_capacity_pools (id, recycler_org_id, pool_code, total_kg, reserved_kg, is_active, version, updated_at)
    SELECT m.new_id, p.recycler_org_id, p.pool_code, p.total_kg, p.reserved_kg, p.is_active,
           p.version + 1, GREATEST(repaired_at, p.updated_at)
    FROM repair_pool_rows p JOIN repair_pools m ON p.id = m.old_id;
    UPDATE recycler_category_capabilities c
    JOIN repair_capabilities m ON c.id = m.old_id
    JOIN repair_pools p ON c.capacity_pool_id = p.old_id
    SET c.id = m.new_id, c.capacity_pool_id = p.new_id, c.version = c.version + 1,
        c.updated_at = GREATEST(repaired_at, c.updated_at);
    UPDATE recycler_service_zones z JOIN repair_zones m ON z.id = m.old_id
    SET z.id = m.new_id, z.version = z.version + 1,
        z.updated_at = GREATEST(repaired_at, z.updated_at);
    -- Any additional foreign-key reference causes an error and full rollback.
    DELETE p FROM recycler_capacity_pools p JOIN repair_pools m ON p.id = m.old_id;
    COMMIT;
    SELECT 'REPAIRED' AS repair_status, 2 AS pools, 8 AS capabilities, 10 AS service_zones;
END//
CALL repair_ewcsb129_config_ids()//
DROP PROCEDURE repair_ewcsb129_config_ids//
DROP TEMPORARY TABLE repair_pool_rows, repair_ids, repair_zones, repair_capabilities, repair_pools//

-- No automatic rollback: restoring invalid IDs could corrupt later matching history.
