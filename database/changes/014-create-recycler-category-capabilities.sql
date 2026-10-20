--liquibase formatted sql
--changeset team5:EWCSB2-014 dbms:mysql
--comment: DESIGN DRAFT. Exact category/compatibility and same-owner pool.
CREATE TABLE recycler_category_capabilities (
    id VARCHAR(36) NOT NULL,
    recycler_org_id VARCHAR(32) NOT NULL,
    category VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    accepted_conditions_json JSON NOT NULL,
    supports_data_bearing BOOLEAN NOT NULL,
    is_active BOOLEAN NOT NULL,
    capacity_pool_id VARCHAR(36) NOT NULL,
    version BIGINT NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_recycler_category_capabilities PRIMARY KEY (id),
    CONSTRAINT uq_recycler_category UNIQUE (recycler_org_id, category),
    CONSTRAINT fk_recycler_category_profile FOREIGN KEY (recycler_org_id)
        REFERENCES recycler_matching_profiles (recycler_org_id),
    CONSTRAINT fk_recycler_category_owned_pool
        FOREIGN KEY (capacity_pool_id, recycler_org_id)
        REFERENCES recycler_capacity_pools (id, recycler_org_id),
    CONSTRAINT ck_recycler_category_code CHECK (category IN (
        'ICT_EQUIPMENT','LARGE_APPLIANCE','BATTERIES','CONSUMER_ELECTRONICS')),
    CONSTRAINT ck_recycler_category_values CHECK (
        supports_data_bearing IN (0,1) AND is_active IN (0,1) AND version >= 1),
    CONSTRAINT ck_recycler_category_conditions CHECK (
        JSON_TYPE(accepted_conditions_json) = 'ARRAY'
        AND JSON_LENGTH(accepted_conditions_json) >= 1
        AND JSON_CONTAINS('["FUNCTIONAL","REPAIRABLE","END_OF_LIFE"]',
                          accepted_conditions_json) = 1),
    INDEX idx_recycler_category_pool (capacity_pool_id, recycler_org_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- API: conditions are a duplicate-free array of scalar canonical strings.
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE recycler_category_capabilities;
