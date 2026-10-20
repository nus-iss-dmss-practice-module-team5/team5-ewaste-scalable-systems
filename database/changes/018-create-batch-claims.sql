--liquibase formatted sql
--changeset team5:EWCSB3-018 dbms:mysql
--comment: DESIGN DRAFT. Apply after C1 006-010 and C2 011-017.
CREATE TABLE batch_claims (
    id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    claim_epoch BIGINT UNSIGNED NOT NULL DEFAULT 1,
    recycler_org_id VARCHAR(32) NOT NULL,
    claimed_by VARCHAR(32) NOT NULL,
    claim_status VARCHAR(32) COLLATE utf8mb4_0900_as_cs
        NOT NULL DEFAULT 'ACCEPTED',
    idempotency_key VARCHAR(64) NOT NULL,
    claimed_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    superseded_at DATETIME(6) NULL,
    notes VARCHAR(255) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT pk_batch_claims PRIMARY KEY (id),
    CONSTRAINT uk_batch_claim_epoch UNIQUE (batch_id, claim_epoch),
    CONSTRAINT uk_claim_idempotency UNIQUE (idempotency_key),
    CONSTRAINT uq_claim_id_batch UNIQUE (id, batch_id),
    CONSTRAINT uq_claim_id_batch_epoch UNIQUE (id, batch_id, claim_epoch),
    CONSTRAINT fk_batch_claims_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_batch_claims_org FOREIGN KEY (recycler_org_id)
        REFERENCES organisations (organisation_id),
    CONSTRAINT fk_batch_claims_user
        FOREIGN KEY (claimed_by) REFERENCES users (user_id),
    CONSTRAINT chk_claim_status CHECK (
        claim_status IN ('ACCEPTED', 'SUPERSEDED', 'REJECTED')),
    CONSTRAINT ck_claim_epoch CHECK (claim_epoch >= 1),
    CONSTRAINT ck_claim_key_length CHECK (
        CHAR_LENGTH(idempotency_key) BETWEEN 16 AND 64),
    CONSTRAINT ck_claim_created_time CHECK (created_at >= claimed_at),
    CONSTRAINT ck_claim_superseded_time CHECK (
        (claim_status = 'SUPERSEDED' AND superseded_at IS NOT NULL
         AND superseded_at >= claimed_at)
        OR (claim_status IN ('ACCEPTED', 'REJECTED')
            AND superseded_at IS NULL)),
    INDEX idx_claims_recycler_org (recycler_org_id, claimed_at),
    INDEX idx_claims_user (claimed_by)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci;
-- Global claim-key UQ uses ai_ci; durable actor/command replay uses as_cs.
-- API: valid ASCII key; current matched recycler; ACCEPTED winners only.
-- REJECTED is retained schema vocabulary, not a concurrent-loser insert.
-- FKs omit actions: InnoDB RESTRICT, never cascading history deletion.
-- DESTRUCTIVE rollback only after empty-data gates in migration-note.md.
-- Roll back 020b, 020a and 019 first; no live/history-bearing rollback.
--rollback DROP TABLE batch_claims;
