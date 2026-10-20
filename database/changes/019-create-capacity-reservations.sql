--liquibase formatted sql
--changeset team5:EWCSB3-019 dbms:mysql
--comment: DESIGN DRAFT. One full-weight reservation for each accepted claim.
CREATE TABLE capacity_reservations (
    id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    claim_id VARCHAR(36) NOT NULL,
    capacity_pool_id VARCHAR(36) NOT NULL,
    reserved_kg DECIMAL(8,2) NOT NULL,
    status VARCHAR(16) COLLATE utf8mb4_0900_as_cs NOT NULL,
    reserved_at DATETIME(6) NOT NULL,
    released_at DATETIME(6) NULL,
    release_reason VARCHAR(255) NULL,
    release_command_id VARCHAR(36) NULL,
    version BIGINT NOT NULL,
    CONSTRAINT pk_capacity_reservations PRIMARY KEY (id),
    CONSTRAINT uq_reservation_claim UNIQUE (claim_id),
    CONSTRAINT fk_reservation_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_reservation_claim_batch
        FOREIGN KEY (claim_id, batch_id)
        REFERENCES batch_claims (id, batch_id),
    CONSTRAINT fk_reservation_pool
        FOREIGN KEY (capacity_pool_id)
        REFERENCES recycler_capacity_pools (id),
    CONSTRAINT fk_reservation_release_command
        FOREIGN KEY (release_command_id) REFERENCES command_idempotency (id),
    CONSTRAINT ck_reservation_weight CHECK (
        reserved_kg BETWEEN 0.10 AND 50000.00),
    CONSTRAINT ck_reservation_version CHECK (version >= 1),
    CONSTRAINT ck_reservation_status CHECK (
        status IN ('RESERVED', 'RELEASED')),
    CONSTRAINT ck_reservation_release CHECK (
        (status = 'RESERVED' AND released_at IS NULL
         AND release_reason IS NULL AND release_command_id IS NULL)
        OR (status = 'RELEASED' AND released_at IS NOT NULL
            AND released_at >= reserved_at AND release_reason IS NOT NULL
            AND CHAR_LENGTH(TRIM(release_reason)) > 0
            AND release_command_id IS NOT NULL)),
    INDEX idx_reservation_batch (batch_id),
    INDEX idx_reservation_claim_batch (claim_id, batch_id),
    INDEX idx_reservation_pool_state (capacity_pool_id, status),
    INDEX idx_reservation_release_command (release_command_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci;
-- API: claim/pool same recycler; reserved_kg equals immutable batch weight.
-- Lock batch then pool; reserve and update pool in the claim transaction.
-- No reservation release for collector failure or collector replacement.
-- RELEASED vocabulary does not invent an approved release/expiry policy.
-- DESTRUCTIVE rollback only after empty-data gates in migration-note.md.
--rollback DROP TABLE capacity_reservations;
