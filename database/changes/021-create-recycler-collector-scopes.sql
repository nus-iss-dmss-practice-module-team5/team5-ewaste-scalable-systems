--liquibase formatted sql
--changeset team5:EWCSB4-021 dbms:mysql
--comment: DESIGN DRAFT. Apply after the approved C1-C3 changesets.
CREATE TABLE recycler_collector_scopes (
    id VARCHAR(36) NOT NULL,
    recycler_org_id VARCHAR(32) NOT NULL,
    collector_org_id VARCHAR(32) NOT NULL,
    zone VARCHAR(16) COLLATE utf8mb4_0900_as_cs NOT NULL,
    is_active BOOLEAN NOT NULL,
    version BIGINT NOT NULL,
    valid_from DATETIME(6) NOT NULL,
    valid_until DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_recycler_collector_scopes PRIMARY KEY (id),
    CONSTRAINT uq_collector_scope_pair_zone
        UNIQUE (recycler_org_id, collector_org_id, zone),
    CONSTRAINT uq_collector_scope_id_pair
        UNIQUE (id, recycler_org_id, collector_org_id),
    CONSTRAINT fk_collector_scope_recycler
        FOREIGN KEY (recycler_org_id)
        REFERENCES organisations (organisation_id),
    CONSTRAINT fk_collector_scope_collector
        FOREIGN KEY (collector_org_id)
        REFERENCES organisations (organisation_id),
    CONSTRAINT ck_collector_scope_zone CHECK (
        zone IN ('NORTH', 'SOUTH', 'EAST', 'WEST', 'CENTRAL')),
    CONSTRAINT ck_collector_scope_active CHECK (is_active IN (0, 1)),
    CONSTRAINT ck_collector_scope_version CHECK (version >= 1),
    CONSTRAINT ck_collector_scope_interval CHECK (
        valid_until IS NULL OR valid_until > valid_from),
    CONSTRAINT ck_collector_scope_updated CHECK (updated_at >= created_at),
    INDEX idx_collector_scope_lookup
        (collector_org_id, zone, is_active, recycler_org_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci;
-- API: ACTIVE PROCESSING_FACILITY and COLLECTION_OPERATOR organisations;
-- valid_from <= command time < valid_until (when present), and batch zone.
-- Retain a used scope identity/pair/zone; disable rather than delete it.
-- Record observed scope version in existing action/audit details_json.
-- No referential actions specified: InnoDB RESTRICT, never CASCADE.
-- DESTRUCTIVE rollback only after gates in ../migration-order.md pass.
--rollback DROP TABLE recycler_collector_scopes;
