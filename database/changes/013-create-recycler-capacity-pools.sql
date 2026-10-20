--liquibase formatted sql
--changeset team5:EWCSB2-013 dbms:mysql
--comment: DESIGN DRAFT. Shared owned capacity; matching never reserves it.
CREATE TABLE recycler_capacity_pools (
    id VARCHAR(36) NOT NULL,
    recycler_org_id VARCHAR(32) NOT NULL,
    pool_code VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    total_kg DECIMAL(12,2) NOT NULL,
    reserved_kg DECIMAL(12,2) NOT NULL,
    is_active BOOLEAN NOT NULL,
    version BIGINT NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_recycler_capacity_pools PRIMARY KEY (id),
    CONSTRAINT uq_recycler_pool_code UNIQUE (recycler_org_id, pool_code),
    CONSTRAINT uq_recycler_pool_owner UNIQUE (id, recycler_org_id),
    CONSTRAINT fk_recycler_pool_org
        FOREIGN KEY (recycler_org_id) REFERENCES organisations (organisation_id),
    CONSTRAINT ck_recycler_pool_values CHECK (
        total_kg >= 0 AND reserved_kg >= 0 AND reserved_kg <= total_kg
        AND is_active IN (0,1) AND version >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- API: available weight = total_kg - reserved_kg. Exact decimals; no rounding.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE recycler_capacity_pools;
