--liquibase formatted sql
--changeset team5:EWCSB2-012 dbms:mysql
--comment: DESIGN DRAFT. Current profile; organisations remains approval authority.
CREATE TABLE recycler_matching_profiles (
    recycler_org_id VARCHAR(32) NOT NULL,
    is_active BOOLEAN NOT NULL,
    version BIGINT NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_recycler_matching_profiles PRIMARY KEY (recycler_org_id),
    CONSTRAINT fk_recycler_profile_org
        FOREIGN KEY (recycler_org_id) REFERENCES organisations (organisation_id),
    CONSTRAINT ck_recycler_profile_values CHECK (
        is_active IN (0,1) AND version >= 1 AND updated_at >= created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- API: require ACTIVE PROCESSING_FACILITY; advance version on configuration edits.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE recycler_matching_profiles;
