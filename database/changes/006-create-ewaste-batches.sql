--liquibase formatted sql

--changeset team5:EWCSB1-006 dbms:mysql
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:2 SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('organisations', 'users');
--comment: DESIGN DRAFT. Create the 19-field batch aggregate after Sprint 1.
-- Require the existing parent tables; no seed identity is a DDL dependency.
CREATE TABLE ewaste_batches (
    id                      VARCHAR(36) NOT NULL,
    organization_id         VARCHAR(32) NOT NULL,
    created_by              VARCHAR(32) NOT NULL,
    status                  VARCHAR(32) COLLATE utf8mb4_0900_as_cs
                                NOT NULL DEFAULT 'DRAFT',
    category                VARCHAR(32) COLLATE utf8mb4_0900_as_cs NULL,
    quantity                INT UNSIGNED NULL,
    estimated_weight_kg     DECIMAL(8,2) NULL,
    condition_rating        VARCHAR(32) COLLATE utf8mb4_0900_as_cs NULL,
    is_data_bearing         BOOLEAN NOT NULL DEFAULT FALSE,
    zone                    VARCHAR(16) COLLATE utf8mb4_0900_as_cs NULL,
    collection_deadline     DATETIME(6) NULL,
    notes                   VARCHAR(500) NULL,
    claim_epoch             BIGINT UNSIGNED NOT NULL DEFAULT 1,
    current_claim_id        VARCHAR(36) NULL DEFAULT NULL,
    current_assignment_id   VARCHAR(36) NULL DEFAULT NULL,
    version                 INT UNSIGNED NOT NULL DEFAULT 1,
    submitted_at            DATETIME(6) NULL,
    created_at              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                                ON UPDATE CURRENT_TIMESTAMP(6),

    CONSTRAINT pk_ewaste_batches PRIMARY KEY (id),
    CONSTRAINT fk_batches_organisation
        FOREIGN KEY (organization_id)
        REFERENCES organisations (organisation_id),
    CONSTRAINT fk_batches_creator
        FOREIGN KEY (created_by) REFERENCES users (user_id),

    -- Separate staging guards: replace each with its FK in the later slice.
    CONSTRAINT ck_batches_c1_claim_null
        CHECK (current_claim_id IS NULL),
    CONSTRAINT ck_batches_c1_assignment_null
        CHECK (current_assignment_id IS NULL),

    INDEX idx_batches_org_status
        (organization_id, status, created_at, id),
    INDEX idx_batches_org_created (organization_id, created_at, id),
    INDEX idx_batches_creator (created_by)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- Apply 007-010 before enabling any C1 writers. No IF NOT EXISTS drift hiding.
-- DESTRUCTIVE ROLLBACK: reviewed disposable/pre-business-write databases only.
-- Roll back 010, 009, 008 and 007 first. Never erase accepted batch history.
--rollback DROP TABLE ewaste_batches;
