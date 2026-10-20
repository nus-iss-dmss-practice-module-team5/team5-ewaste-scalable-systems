--liquibase formatted sql

--changeset team5:EWCSB126-104 dbms:mysql context:@c1-fixtures labels:synthetic-test-data
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:2 SELECT COUNT(*) FROM users WHERE (user_id = 'USR-003' AND organisation_id = 'DON-001' AND role_code = 'DONOR' AND status = 'ACTIVE') OR (user_id = 'USR-004' AND organisation_id = 'DON-002' AND role_code = 'DONOR' AND status = 'ACTIVE');
--comment: Explicit opt-in C1 fixtures. Use context-filter=seed,c1-fixtures on isolated test databases.
-- Fixed UUIDv4 values and UTC dates; no NOW(), random IDs, UPSERT or history deletion.
-- Liquibase runs this transaction once; a second update is a no-op.
SET time_zone = '+00:00';

INSERT INTO ewaste_batches (
    id, organization_id, created_by, status, category, quantity,
    estimated_weight_kg, condition_rating, is_data_bearing, zone,
    collection_deadline, notes, claim_epoch, current_claim_id,
    current_assignment_id, version, submitted_at, created_at, updated_at
) VALUES
('b1260000-0000-4000-8000-000000000001', 'DON-001', 'USR-003', 'DRAFT',
 NULL, NULL, NULL, NULL, FALSE, NULL, NULL, NULL,
 1, NULL, NULL, 1, NULL, '2026-09-01 00:00:00.000000', '2026-09-01 00:00:00.000000'),
('b1260000-0000-4000-8000-000000000002', 'DON-002', 'USR-004', 'DRAFT',
 'BATTERIES', 1, NULL, NULL, TRUE, 'WEST', NULL, 'Partial synthetic draft',
 1, NULL, NULL, 1, NULL, '2026-09-01 00:00:00.000000', '2026-09-01 00:00:00.000000'),
('b1260000-0000-4000-8000-000000000003', 'DON-001', 'USR-003', 'SUBMITTED',
 'ICT_EQUIPMENT', 1, 0.10, 'FUNCTIONAL', FALSE, 'NORTH', '2026-09-03 01:00:00.000000', NULL,
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000004', 'DON-002', 'USR-004', 'SUBMITTED',
 'LARGE_APPLIANCE', 100000, 50000.00, 'END_OF_LIFE', TRUE, 'SOUTH', '2026-11-30 01:00:00.000000', REPEAT('界', 500),
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000005', 'DON-001', 'USR-003', 'SUBMITTED',
 'BATTERIES', 10, 12.34, 'REPAIRABLE', TRUE, 'EAST', '2026-09-04 01:00:00.000000', '',
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000006', 'DON-002', 'USR-004', 'SUBMITTED',
 'CONSUMER_ELECTRONICS', 25, 250.50, 'FUNCTIONAL', FALSE, 'WEST', '2026-09-05 01:00:00.000000', 'Synthetic electronics',
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000007', 'DON-001', 'USR-003', 'SUBMITTED',
 'ICT_EQUIPMENT', 5, 100.00, 'REPAIRABLE', TRUE, 'CENTRAL', '2026-09-08 01:00:00.000000', 'Synthetic ICT',
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000');

-- These command labels and hashes describe fixture history, not a new HTTP contract.
-- retain_until is a fixed test value, not a production retention policy.
INSERT INTO command_idempotency (
    id, actor_user_id, actor_scope, command_name, idempotency_key, request_hash,
    batch_id, state, response_status, response_json, created_at, completed_at, retain_until
)
SELECT CONCAT('c1260000', SUBSTRING(id, 9)), created_by, CONCAT('user:', created_by),
       'CreateDraft', CONCAT('c1-fixture-create:', id), SHA2(CONCAT('c1-fixture-create:', id), 256),
       id, 'COMPLETED', 201,
       JSON_OBJECT('batchId', id, 'status', 'DRAFT', 'version', 1),
       created_at, created_at, '2027-09-01 00:00:00.000000'
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007');

INSERT INTO batch_audit_events (
    id, batch_id, command_id, actor_user_id, actor_org_id, event_type,
    from_status, to_status, batch_version, sequence_in_command,
    occurred_at, correlation_id, details_json
)
SELECT CONCAT('a1260000', SUBSTRING(id, 9)), id, CONCAT('c1260000', SUBSTRING(id, 9)),
       created_by, organization_id, 'DraftSaved', 'DRAFT', 'DRAFT', 1, 1,
       created_at, CONCAT('c1-fixture-create:', id), JSON_OBJECT('operation', 'CREATE')
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007');

INSERT INTO command_idempotency (
    id, actor_user_id, actor_scope, command_name, idempotency_key, request_hash,
    batch_id, state, response_status, response_json, created_at, completed_at, retain_until
)
SELECT CONCAT('c1260001', SUBSTRING(id, 9)), created_by, CONCAT('user:', created_by),
       'SubmitBatch', CONCAT('c1-fixture-submit:', id), SHA2(CONCAT('c1-fixture-submit:', id), 256),
       id, 'COMPLETED', 200,
       JSON_OBJECT('batchId', id, 'status', 'SUBMITTED', 'version', 2,
                   'eventId', CONCAT('e1260000', SUBSTRING(id, 9)), 'eventState', 'PENDING'),
       submitted_at, submitted_at, '2027-09-01 00:00:00.000000'
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007') AND status = 'SUBMITTED';

INSERT INTO batch_audit_events (
    id, batch_id, command_id, actor_user_id, actor_org_id, event_type,
    from_status, to_status, batch_version, sequence_in_command,
    occurred_at, correlation_id, details_json
)
SELECT CONCAT('a1260001', SUBSTRING(id, 9)), id, CONCAT('c1260001', SUBSTRING(id, 9)),
       created_by, organization_id, 'RequestSubmitted', 'DRAFT', 'SUBMITTED', version, 1,
       submitted_at, CONCAT('c1-fixture-submit:', id), JSON_OBJECT('operation', 'SUBMIT')
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007') AND status = 'SUBMITTED';

INSERT INTO event_outbox (
    event_id, batch_id, command_id, event_type, topic, schema_version,
    aggregate_version, sequence_in_command, partition_key, payload_json,
    correlation_id, occurred_at, created_at, publish_state, attempt_count, next_attempt_at
)
SELECT CONCAT('e1260000', SUBSTRING(id, 9)), id, CONCAT('c1260001', SUBSTRING(id, 9)),
       'RequestSubmitted', 'ewaste.batch.events', 1, version, 1, id,
       JSON_OBJECT(
           'event_id', CONCAT('e1260000', SUBSTRING(id, 9)), 'event_type', 'RequestSubmitted',
           'schema_version', 1, 'command_id', CONCAT('c1260001', SUBSTRING(id, 9)),
           'batch_id', id, 'batch_version', version, 'claim_epoch', CAST(claim_epoch AS CHAR),
           'sequence_in_command', 1, 'occurred_at', DATE_FORMAT(submitted_at, '%Y-%m-%dT%H:%i:%s.%fZ'),
           'correlation_id', CONCAT('c1-fixture-submit:', id),
           'data', JSON_OBJECT(
               'organization_id', organization_id,
               'submitted_at', DATE_FORMAT(submitted_at, '%Y-%m-%dT%H:%i:%s.%fZ'),
               'category', category, 'quantity', quantity,
               'estimated_weight_kg', CAST(estimated_weight_kg AS CHAR),
               'condition_rating', condition_rating,
               'is_data_bearing', JSON_EXTRACT(IF(is_data_bearing, 'true', 'false'), '$'),
               'zone', zone, 'collection_deadline', DATE_FORMAT(collection_deadline, '%Y-%m-%dT%H:%i:%s.%fZ'))),
       CONCAT('c1-fixture-submit:', id), submitted_at, submitted_at, 'PENDING', 0, submitted_at
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007') AND status = 'SUBMITTED';

-- No destructive rollback: fixture command/audit/outbox history is retained.
-- The test runner disposes only of its own isolated container volume.
