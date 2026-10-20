--liquibase formatted sql
--changeset team5:EWCSB3-027 dbms:mysql
--comment: Processing storage aligned with D1-D4. New, not-yet-deployed changeset only.
ALTER TABLE command_idempotency ADD CONSTRAINT uq_command_id_batch UNIQUE (id, batch_id);
CREATE TABLE batch_receipts (
    receipt_id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    facility_org_id VARCHAR(32) NOT NULL,
    verified_by VARCHAR(32) NOT NULL,
    actual_category VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    actual_item_count INT UNSIGNED NOT NULL,
    actual_weight_kg DECIMAL(10,2) NOT NULL,
    command_id VARCHAR(36) NOT NULL,
    correlation_id VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    version INT UNSIGNED NOT NULL DEFAULT 1,
    verified_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT pk_batch_receipts PRIMARY KEY (receipt_id),
    CONSTRAINT uq_receipt_batch UNIQUE (batch_id),
    CONSTRAINT uq_receipt_command UNIQUE (command_id),
    CONSTRAINT uq_receipt_source UNIQUE (receipt_id, batch_id, facility_org_id, version, actual_weight_kg),
    CONSTRAINT fk_receipt_batch FOREIGN KEY (batch_id) REFERENCES ewaste_batches(id),
    CONSTRAINT fk_receipt_facility_org FOREIGN KEY (facility_org_id) REFERENCES organisations(organisation_id),
    CONSTRAINT fk_receipt_verified_by FOREIGN KEY (verified_by) REFERENCES users(user_id),
    CONSTRAINT fk_receipt_command_batch FOREIGN KEY (command_id, batch_id) REFERENCES command_idempotency(id, batch_id),
    CONSTRAINT ck_receipt_category CHECK (actual_category IN ('ICT_EQUIPMENT','LARGE_APPLIANCE','BATTERIES','CONSUMER_ELECTRONICS')),
    CONSTRAINT ck_receipt_item_count CHECK (actual_item_count BETWEEN 1 AND 100000),
    CONSTRAINT ck_receipt_weight CHECK (actual_weight_kg BETWEEN 0.10 AND 50000.00),
    CONSTRAINT ck_receipt_version CHECK (version >= 1),
    CONSTRAINT ck_receipt_correlation CHECK (CHAR_LENGTH(TRIM(correlation_id)) BETWEEN 1 AND 128),
    INDEX idx_receipt_facility (facility_org_id, verified_at),
    INDEX idx_receipt_verified_by (verified_by)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- Application transactions own authorization, state/version checks and audit/outbox writes.
-- Rollback is destructive: disposable/pre-write databases only. See docs/processing-storage.md.
--rollback DROP TABLE batch_receipts;
--rollback ALTER TABLE command_idempotency DROP INDEX uq_command_id_batch;
