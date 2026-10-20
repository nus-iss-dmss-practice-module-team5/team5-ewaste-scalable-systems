--liquibase formatted sql
--changeset team5:EWCSB2-011 dbms:mysql
--comment: DESIGN DRAFT. Immutable versioned policy; implementation owner Unassigned.
CREATE TABLE matching_rule_sets (
    id VARCHAR(36) NOT NULL,
    version VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    rules_json JSON NOT NULL,
    effective_from DATETIME(6) NOT NULL,
    retired_at DATETIME(6) NULL,
    created_by VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_matching_rule_sets PRIMARY KEY (id),
    CONSTRAINT uq_matching_rule_version UNIQUE (version),
    CONSTRAINT fk_matching_rule_creator
        FOREIGN KEY (created_by) REFERENCES users (user_id),
    CONSTRAINT ck_matching_rule_window CHECK (
        retired_at IS NULL OR retired_at > effective_from),
    CONSTRAINT ck_matching_rule_object CHECK (JSON_TYPE(rules_json) = 'OBJECT'),
    INDEX idx_matching_rule_activation (effective_from, retired_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- API: validate complete policy, nonoverlapping activation and immutable versions.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE matching_rule_sets;
