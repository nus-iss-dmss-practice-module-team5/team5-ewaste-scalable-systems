--liquibase formatted sql
--changeset team5:EWCSB4-023 dbms:mysql
--comment: DESIGN DRAFT. One immutable terminal pickup outcome per attempt.
CREATE TABLE batch_handoffs (
    id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    assignment_id VARCHAR(36) NOT NULL,
    collector_user_id VARCHAR(32) NOT NULL,
    collector_org_id VARCHAR(32) NOT NULL,
    pickup_status VARCHAR(24) COLLATE utf8mb4_0900_as_cs NOT NULL,
    donor_representative_name VARCHAR(100) NULL,
    actual_item_count INT UNSIGNED NULL,
    verification_hash CHAR(64) COLLATE utf8mb4_0900_as_cs NULL,
    failure_reason VARCHAR(32) COLLATE utf8mb4_0900_as_cs NULL,
    quantity_discrepancy_reason VARCHAR(255) NULL,
    notes VARCHAR(500) NULL,
    pickup_occurred_at DATETIME(6) NOT NULL,
    recorded_at DATETIME(6) NOT NULL,
    collected_at DATETIME(6) NULL,
    command_id VARCHAR(36) NOT NULL,
    correlation_id VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_batch_handoffs PRIMARY KEY (id),
    CONSTRAINT uq_handoff_assignment UNIQUE (assignment_id),
    CONSTRAINT uq_handoff_command UNIQUE (command_id),
    CONSTRAINT fk_handoff_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_handoff_assignment_actor
        FOREIGN KEY (assignment_id, batch_id,
                     collector_user_id, collector_org_id)
        REFERENCES batch_assignments
            (id, batch_id, collector_user_id, collector_org_id),
    CONSTRAINT fk_handoff_command
        FOREIGN KEY (command_id) REFERENCES command_idempotency (id),
    CONSTRAINT ck_handoff_outcome CHECK (
        (pickup_status = 'COLLECTED'
         AND donor_representative_name IS NOT NULL
         AND actual_item_count IS NOT NULL AND verification_hash IS NOT NULL
         AND collected_at IS NOT NULL
         AND collected_at = pickup_occurred_at AND failure_reason IS NULL)
        OR (pickup_status = 'FAILED_COLLECTION'
            AND collected_at IS NULL AND failure_reason IS NOT NULL
            AND failure_reason IN ('DONOR_UNAVAILABLE', 'INCORRECT_ITEMS',
                'ACCESS_DENIED', 'DAMAGED_HAZARDOUS', 'SAFETY_CANCEL'))),
    CONSTRAINT ck_handoff_donor_name CHECK (
        donor_representative_name IS NULL
        OR CHAR_LENGTH(TRIM(donor_representative_name)) BETWEEN 1 AND 100),
    CONSTRAINT ck_handoff_count CHECK (
        actual_item_count IS NULL OR actual_item_count BETWEEN 1 AND 100000),
    CONSTRAINT ck_handoff_hash CHECK (
        verification_hash IS NULL
        OR REGEXP_LIKE(verification_hash, '^[0-9A-Fa-f]{64}$', 'c')),
    CONSTRAINT ck_handoff_discrepancy CHECK (
        quantity_discrepancy_reason IS NULL
        OR CHAR_LENGTH(TRIM(quantity_discrepancy_reason)) BETWEEN 1 AND 255),
    CONSTRAINT ck_handoff_times CHECK (
        pickup_occurred_at <= recorded_at AND recorded_at <= created_at),
    INDEX idx_handoff_assignment_actor
        (assignment_id, batch_id, collector_user_id, collector_org_id),
    INDEX idx_handoff_batch_history (batch_id, pickup_occurred_at, id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci;
-- API: current assignment/actor; assigned_at <= pickup_occurred_at;
-- Proposed API policy: count mismatch requires discrepancy reason.
-- Confirm that policy before implementation; no cross-table SQL CHECK.
-- Failure may retain optional observed count/proof; it is not collection.
-- Do not require fabricated donor, count or hash on failed pickup.
-- RejectAssignment creates no handoff; do not label rejection as a pickup.
-- One command inserts at most one handoff. Actor tuple must match parent.
-- Domain update + action/audit/outbox + response commit atomically.
-- Table is append-only after commit: runtime privileges and command guards.
-- DESTRUCTIVE rollback only after gates in ../migration-order.md pass.
--rollback DROP TABLE batch_handoffs;
