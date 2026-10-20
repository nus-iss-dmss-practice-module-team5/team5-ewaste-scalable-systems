--liquibase formatted sql
--changeset team5:EWCSB2-015 dbms:mysql
--comment: DESIGN DRAFT. Explicit zone membership; no inferred adjacency.
CREATE TABLE recycler_service_zones (
    id VARCHAR(36) NOT NULL,
    recycler_org_id VARCHAR(32) NOT NULL,
    zone VARCHAR(16) COLLATE utf8mb4_0900_as_cs NOT NULL,
    minimum_lead_minutes INT UNSIGNED NOT NULL,
    is_active BOOLEAN NOT NULL,
    version BIGINT NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_recycler_service_zones PRIMARY KEY (id),
    CONSTRAINT uq_recycler_service_zone UNIQUE (recycler_org_id, zone),
    CONSTRAINT fk_recycler_service_profile FOREIGN KEY (recycler_org_id)
        REFERENCES recycler_matching_profiles (recycler_org_id),
    CONSTRAINT ck_recycler_service_zone CHECK (
        zone IN ('NORTH','SOUTH','EAST','WEST','CENTRAL')),
    CONSTRAINT ck_recycler_service_values CHECK (is_active IN (0,1) AND version >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- API: calculate feasible_at without overflow; version every coverage edit.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE recycler_service_zones;
