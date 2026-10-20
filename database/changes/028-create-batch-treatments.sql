--liquibase formatted sql
--changeset team5:EWCSB3-028 dbms:mysql
--comment: Processing storage aligned with D1-D4. New, not-yet-deployed changeset only.
CREATE TABLE batch_treatments (
    treatment_id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    facility_org_id VARCHAR(32) NOT NULL,
    treated_by VARCHAR(32) NOT NULL,
    receipt_id VARCHAR(36) NOT NULL,
    receipt_version INT UNSIGNED NOT NULL,
    received_weight_kg DECIMAL(10,2) NOT NULL,
    reused_kg DECIMAL(10,2) NULL,
    recycled_kg DECIMAL(10,2) NULL,
    disposed_kg DECIMAL(10,2) NULL,
    unknown_kg DECIMAL(10,2) GENERATED ALWAYS AS (
        CASE WHEN reused_kg IS NULL THEN received_weight_kg
        ELSE received_weight_kg - reused_kg - recycled_kg - disposed_kg END) STORED,
    evidence_id VARCHAR(36) NULL,
    evidence_stage VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL DEFAULT 'TREATMENT',
    command_id VARCHAR(36) NOT NULL,
    correlation_id VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    version INT UNSIGNED NOT NULL DEFAULT 1,
    completed_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT pk_batch_treatments PRIMARY KEY (treatment_id),
    CONSTRAINT uq_treatment_batch UNIQUE (batch_id),
    CONSTRAINT uq_treatment_command UNIQUE (command_id),
    CONSTRAINT uq_treatment_source UNIQUE (treatment_id, batch_id, facility_org_id, version),
    CONSTRAINT fk_treatment_batch FOREIGN KEY (batch_id) REFERENCES ewaste_batches(id),
    CONSTRAINT fk_treatment_facility_org FOREIGN KEY (facility_org_id) REFERENCES organisations(organisation_id),
    CONSTRAINT fk_treatment_treated_by FOREIGN KEY (treated_by) REFERENCES users(user_id),
    CONSTRAINT fk_treatment_receipt FOREIGN KEY (receipt_id,batch_id,facility_org_id,receipt_version,received_weight_kg)
        REFERENCES batch_receipts(receipt_id,batch_id,facility_org_id,version,actual_weight_kg),
    CONSTRAINT fk_treatment_evidence FOREIGN KEY (evidence_id,batch_id,facility_org_id,evidence_stage)
        REFERENCES batch_evidence(evidence_id,batch_id,organisation_id,lifecycle_stage),
    CONSTRAINT fk_treatment_command_batch FOREIGN KEY (command_id,batch_id) REFERENCES command_idempotency(id,batch_id),
    CONSTRAINT ck_treatment_all_or_none CHECK (
        (reused_kg IS NULL AND recycled_kg IS NULL AND disposed_kg IS NULL)
        OR (reused_kg IS NOT NULL AND recycled_kg IS NOT NULL AND disposed_kg IS NOT NULL)),
    CONSTRAINT ck_treatment_amounts CHECK (
        reused_kg IS NULL OR (reused_kg >= 0 AND recycled_kg >= 0 AND disposed_kg >= 0
        AND reused_kg + recycled_kg + disposed_kg <= received_weight_kg)),
    CONSTRAINT ck_treatment_source_weight CHECK (received_weight_kg BETWEEN 0.10 AND 50000.00),
    CONSTRAINT ck_treatment_version CHECK (version >= 1 AND receipt_version >= 1),
    CONSTRAINT ck_treatment_evidence_stage CHECK (evidence_stage = 'TREATMENT'),
    CONSTRAINT ck_treatment_correlation CHECK (CHAR_LENGTH(TRIM(correlation_id)) BETWEEN 1 AND 128),
    INDEX idx_treatment_facility (facility_org_id, completed_at),
    INDEX idx_treatment_treated_by (treated_by)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- Application transactions own authorization, state/version checks and audit/outbox writes.
-- Rollback is destructive: disposable/pre-write databases only. See docs/processing-storage.md.
--rollback DROP TABLE batch_treatments;
