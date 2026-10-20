--liquibase formatted sql
--changeset team5:EWCSB3-026 dbms:mysql
--comment: Physical evidence attachment metadata for receipts and treatment (Decision D2).
CREATE TABLE batch_evidence (
    evidence_id         VARCHAR(36) NOT NULL,
    batch_id            VARCHAR(36) NOT NULL,
    organisation_id     VARCHAR(32) NOT NULL,
    uploaded_by         VARCHAR(32) NOT NULL,
    lifecycle_stage     VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    original_file_name  VARCHAR(255) NOT NULL,
    stored_object_key   VARCHAR(512) NOT NULL,
    mime_type           VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    file_size_bytes     BIGINT UNSIGNED NOT NULL,
    sha256_hash         CHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    created_at          DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT pk_batch_evidence PRIMARY KEY (evidence_id),
    CONSTRAINT fk_evidence_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id) ON DELETE RESTRICT,
    CONSTRAINT fk_evidence_organisation
        FOREIGN KEY (organisation_id) REFERENCES organisations (organisation_id),
    CONSTRAINT fk_evidence_uploader
        FOREIGN KEY (uploaded_by) REFERENCES users (user_id),
    CONSTRAINT ck_evidence_lifecycle_stage
        CHECK (lifecycle_stage IN ('RECEIPT', 'TREATMENT')),
    CONSTRAINT ck_evidence_mime_type
        CHECK (mime_type IN ('application/pdf', 'image/jpeg', 'image/png')),
    CONSTRAINT ck_evidence_file_size
        CHECK (file_size_bytes > 0 AND file_size_bytes <= 5242880),
    CONSTRAINT ck_evidence_hash
        CHECK (REGEXP_LIKE(sha256_hash, '^[0-9a-fA-F]{64}$', 'c')),
    CONSTRAINT uq_evidence_object UNIQUE (stored_object_key),
    CONSTRAINT uq_evidence_scope UNIQUE (evidence_id, batch_id, organisation_id, lifecycle_stage),
    INDEX idx_evidence_batch_stage (batch_id, lifecycle_stage),
    INDEX idx_evidence_org (organisation_id),
    INDEX idx_evidence_hash (sha256_hash)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;
-- Rollback only before business writes; see docs/processing-storage.md.
--rollback DROP TABLE batch_evidence;
