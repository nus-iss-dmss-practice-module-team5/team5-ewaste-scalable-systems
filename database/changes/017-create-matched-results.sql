--liquibase formatted sql
--changeset team5:EWCSB2-017 dbms:mysql
--comment: DESIGN DRAFT. Every evaluated organisation, including false results.
CREATE TABLE matched_results (
    id VARCHAR(36) NOT NULL,
    decision_id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    recycler_org_id VARCHAR(32) NOT NULL,
    profile_version BIGINT NULL,
    category_match BOOLEAN NOT NULL,
    capability_match BOOLEAN NOT NULL,
    capacity_available BOOLEAN NOT NULL,
    zone_match BOOLEAN NOT NULL,
    deadline_viable BOOLEAN NOT NULL,
    is_matched BOOLEAN NOT NULL,
    available_capacity_kg DECIMAL(12,2) NULL,
    capacity_pool_id VARCHAR(36) NULL,
    capacity_version BIGINT NULL,
    minimum_lead_minutes INT UNSIGNED NULL,
    feasible_at DATETIME(6) NULL,
    reason_code VARCHAR(48) COLLATE utf8mb4_0900_as_cs NOT NULL,
    failed_rules_json JSON NOT NULL,
    evidence_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_matched_results PRIMARY KEY (id),
    CONSTRAINT uq_matched_result_org UNIQUE (decision_id, recycler_org_id),
    CONSTRAINT fk_matched_result_decision_batch
        FOREIGN KEY (decision_id, batch_id)
        REFERENCES matching_decisions (id, batch_id),
    CONSTRAINT fk_matched_result_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_matched_result_org
        FOREIGN KEY (recycler_org_id) REFERENCES organisations (organisation_id),
    CONSTRAINT fk_matched_result_owned_pool
        FOREIGN KEY (capacity_pool_id, recycler_org_id)
        REFERENCES recycler_capacity_pools (id, recycler_org_id),
    CONSTRAINT ck_matched_result_booleans CHECK (
        category_match IN (0,1) AND capability_match IN (0,1)
        AND capacity_available IN (0,1) AND zone_match IN (0,1)
        AND deadline_viable IN (0,1) AND is_matched IN (0,1)
        AND is_matched = (category_match AND capability_match
                         AND capacity_available AND zone_match AND deadline_viable)),
    CONSTRAINT ck_matched_result_snapshots CHECK (
        (profile_version IS NULL OR profile_version >= 1)
        AND (capacity_version IS NULL OR capacity_version >= 1)
        AND (available_capacity_kg IS NULL OR available_capacity_kg >= 0)
        AND (capability_match = 0 OR profile_version IS NOT NULL)
        AND (capacity_available = 0 OR (capacity_pool_id IS NOT NULL
             AND capacity_version IS NOT NULL AND available_capacity_kg IS NOT NULL))
        AND (deadline_viable = 0 OR (minimum_lead_minutes IS NOT NULL
                                    AND feasible_at IS NOT NULL))),
    CONSTRAINT ck_matched_result_reasons CHECK (
        JSON_TYPE(failed_rules_json) = 'ARRAY'
        AND JSON_TYPE(evidence_json) = 'OBJECT'
        AND ((is_matched = 1 AND reason_code = 'ELIGIBLE'
              AND JSON_LENGTH(failed_rules_json) = 0)
          OR (is_matched = 0 AND JSON_LENGTH(failed_rules_json) > 0
              AND reason_code IN ('CATEGORY_UNSUPPORTED','CAPABILITY_UNSUPPORTED',
                'INSUFFICIENT_CAPACITY','CAPACITY_UNAVAILABLE',
                'OUT_OF_SERVICE_ZONE','DEADLINE_UNACHIEVABLE')))),
    INDEX idx_matched_result_parent (decision_id, batch_id),
    INDEX idx_matched_result_opportunity
        (recycler_org_id, is_matched, batch_id, decision_id),
    INDEX idx_matched_result_batch (batch_id),
    INDEX idx_matched_result_pool (capacity_pool_id, recycler_org_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- API: M1-M5 reason precedence, full failure evidence and frozen-snapshot parity.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE matched_results;
