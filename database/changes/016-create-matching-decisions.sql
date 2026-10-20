--liquibase formatted sql
--changeset team5:EWCSB2-016 dbms:mysql
--comment: DESIGN DRAFT. Immutable completed decision, never technical failure.
CREATE TABLE matching_decisions (
    id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    trigger_id VARCHAR(36) NOT NULL,
    trigger_type VARCHAR(24) COLLATE utf8mb4_0900_as_cs NOT NULL,
    batch_version INT UNSIGNED NOT NULL,
    claim_epoch BIGINT UNSIGNED NOT NULL,
    rule_set_id VARCHAR(36) NOT NULL,
    evaluation_at DATETIME(6) NOT NULL,
    input_hash CHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    profile_snapshot_hash CHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    input_snapshot_json JSON NOT NULL,
    outcome VARCHAR(16) COLLATE utf8mb4_0900_as_cs NOT NULL,
    primary_reason VARCHAR(48) COLLATE utf8mb4_0900_as_cs NOT NULL,
    evaluated_count INT UNSIGNED NOT NULL,
    eligible_count INT UNSIGNED NOT NULL,
    correlation_id VARCHAR(128) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    completed_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_matching_decisions PRIMARY KEY (id),
    CONSTRAINT uq_matching_decision_trigger UNIQUE (trigger_id, trigger_type),
    CONSTRAINT uq_matching_decision_batch UNIQUE (id, batch_id),
    CONSTRAINT fk_matching_decision_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_matching_decision_policy
        FOREIGN KEY (rule_set_id) REFERENCES matching_rule_sets (id),
    CONSTRAINT ck_matching_decision_trigger CHECK (
        trigger_type IN ('REQUEST_SUBMITTED','EXPLICIT_RUN','RECOVERY')),
    CONSTRAINT ck_matching_decision_version CHECK (
        batch_version >= 1 AND claim_epoch >= 1),
    CONSTRAINT ck_matching_decision_time CHECK (completed_at >= created_at),
    CONSTRAINT ck_matching_decision_snapshot CHECK (
        JSON_TYPE(input_snapshot_json) = 'OBJECT'),
    CONSTRAINT ck_matching_decision_outcome CHECK (
        eligible_count <= evaluated_count AND (
          (eligible_count > 0 AND outcome = 'MATCHED'
           AND primary_reason = 'ELIGIBLE_EXISTS') OR
          (eligible_count = 0 AND evaluated_count > 0 AND outcome = 'NO_MATCH'
           AND primary_reason = 'NO_ELIGIBLE_ORGANISATION') OR
          (eligible_count = 0 AND evaluated_count = 0 AND outcome = 'NO_MATCH'
           AND primary_reason = 'NO_APPROVED_ORGANISATION'))),
    INDEX idx_matching_decision_current
        (batch_id, claim_epoch, batch_version, outcome, completed_at, id),
    INDEX idx_matching_decision_policy (rule_set_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- RECOVERY is retained schema vocabulary, reserved/inactive in this design.
-- Facade rejects RECOVERY. Collector failure returns to APPROVED without rematch.
-- API: hashes, snapshots, all candidate counts, immutable row, state/version CAS.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE matching_decisions;
