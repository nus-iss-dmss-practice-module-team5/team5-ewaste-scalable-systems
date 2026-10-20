--liquibase formatted sql
--changeset team5:EWCSB3-029 dbms:mysql
--comment: Processing storage aligned with D1-D4. New, not-yet-deployed changeset only.
ALTER TABLE event_outbox ADD CONSTRAINT uq_outbox_event_batch UNIQUE (event_id,batch_id);
CREATE TABLE batch_impact_metrics (
    metric_id VARCHAR(36) NOT NULL COMMENT 'D4 result_id',
    batch_id VARCHAR(36) NOT NULL,
    facility_org_id VARCHAR(32) NOT NULL,
    source_event_id VARCHAR(36) NOT NULL,
    source_batch_version INT UNSIGNED NOT NULL,
    receipt_id VARCHAR(36) NOT NULL,
    receipt_version INT UNSIGNED NOT NULL,
    treatment_id VARCHAR(36) NOT NULL,
    treatment_version INT UNSIGNED NOT NULL,
    received_weight_kg DECIMAL(10,2) NOT NULL,
    reused_kg DECIMAL(10,2) NULL,
    recycled_kg DECIMAL(10,2) NULL,
    disposed_kg DECIMAL(10,2) NULL,
    diverted_kg DECIMAL(10,2) GENERATED ALWAYS AS (reused_kg + recycled_kg) STORED,
    unknown_kg DECIMAL(10,2) GENERATED ALWAYS AS (
        CASE WHEN reused_kg IS NULL THEN received_weight_kg
        ELSE received_weight_kg - reused_kg - recycled_kg - disposed_kg END) STORED,
    data_quality VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    rule_version VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    input_hash CHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    input_snapshot_json JSON NOT NULL,
    result_hash CHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    command_id VARCHAR(36) NOT NULL,
    correlation_id VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    calculated_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT pk_batch_impact_metrics PRIMARY KEY (metric_id),
    CONSTRAINT uq_impact_batch UNIQUE (batch_id),
    CONSTRAINT uq_impact_source_event UNIQUE (source_event_id),
    CONSTRAINT uq_impact_command UNIQUE (command_id),
    CONSTRAINT uq_impact_result_batch UNIQUE (metric_id,batch_id),
    CONSTRAINT fk_impact_batch FOREIGN KEY (batch_id) REFERENCES ewaste_batches(id),
    CONSTRAINT fk_impact_source_event FOREIGN KEY (source_event_id,batch_id) REFERENCES event_outbox(event_id,batch_id),
    CONSTRAINT fk_impact_receipt FOREIGN KEY (receipt_id,batch_id,facility_org_id,receipt_version,received_weight_kg)
        REFERENCES batch_receipts(receipt_id,batch_id,facility_org_id,version,actual_weight_kg),
    CONSTRAINT fk_impact_treatment FOREIGN KEY (treatment_id,batch_id,facility_org_id,treatment_version)
        REFERENCES batch_treatments(treatment_id,batch_id,facility_org_id,version),
    CONSTRAINT fk_impact_command_batch FOREIGN KEY (command_id,batch_id) REFERENCES command_idempotency(id,batch_id),
    CONSTRAINT ck_impact_quality CHECK (data_quality IN ('COMPLETE','PARTIAL','MISSING')),
    CONSTRAINT ck_impact_semantics CHECK (
        (data_quality='MISSING' AND reused_kg IS NULL AND recycled_kg IS NULL AND disposed_kg IS NULL)
        OR (data_quality IN ('COMPLETE','PARTIAL') AND reused_kg IS NOT NULL
            AND recycled_kg IS NOT NULL AND disposed_kg IS NOT NULL
            AND reused_kg >= 0 AND recycled_kg >= 0 AND disposed_kg >= 0
            AND ((data_quality='COMPLETE' AND reused_kg+recycled_kg+disposed_kg=received_weight_kg)
                OR (data_quality='PARTIAL' AND reused_kg+recycled_kg+disposed_kg<received_weight_kg)))),
    CONSTRAINT ck_impact_versions CHECK (source_batch_version>=1 AND receipt_version>=1 AND treatment_version>=1),
    CONSTRAINT ck_impact_hashes CHECK (REGEXP_LIKE(input_hash,'^[0-9a-f]{64}$','c') AND REGEXP_LIKE(result_hash,'^[0-9a-f]{64}$','c')),
    CONSTRAINT ck_impact_snapshot CHECK (JSON_TYPE(input_snapshot_json)='OBJECT'),
    CONSTRAINT ck_impact_rule CHECK (CHAR_LENGTH(TRIM(rule_version))>0),
    CONSTRAINT ck_impact_correlation CHECK (CHAR_LENGTH(TRIM(correlation_id)) BETWEEN 1 AND 128),
    INDEX idx_impact_quality (data_quality,calculated_at),
    INDEX idx_impact_correlation (correlation_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- Receipt/treatment identities are immutable. Go must verify the source event type,
-- current batch version, canonical hashes and every result value against the frozen
-- inputs before the completion transaction. SQL checks cannot establish those facts.
-- Application transactions own authorization, state/version checks and audit/outbox writes.
-- Rollback is destructive: disposable/pre-write databases only. See docs/processing-storage.md.
--rollback DROP TABLE batch_impact_metrics;
--rollback ALTER TABLE event_outbox DROP INDEX uq_outbox_event_batch;
