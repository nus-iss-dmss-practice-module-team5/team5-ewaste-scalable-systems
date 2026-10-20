--liquibase formatted sql
--changeset team5:EWCSB3-030 dbms:mysql
--comment: Deterministic audit anomaly and discrepancy flags detected by analytics.
CREATE TABLE batch_anomalies (
    anomaly_id          VARCHAR(36) NOT NULL,
    batch_id            VARCHAR(36) NOT NULL,
    metric_id           VARCHAR(36) NOT NULL,
    anomaly_code        VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    declared_value      VARCHAR(255) NULL,
    actual_value        VARCHAR(255) NULL,
    discrepancy_delta   DECIMAL(10,2) NULL,
    -- Rule version comes from the referenced immutable result.
    detected_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    created_at          DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT pk_batch_anomalies PRIMARY KEY (anomaly_id),
    CONSTRAINT uq_anomaly_result_code UNIQUE (metric_id, anomaly_code),
    CONSTRAINT fk_anomaly_result FOREIGN KEY (metric_id, batch_id)
        REFERENCES batch_impact_metrics(metric_id, batch_id),
    CONSTRAINT fk_anomaly_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id) ON DELETE RESTRICT,
    CONSTRAINT ck_anomaly_code CHECK (
        anomaly_code IN (
            'CATEGORY_MISMATCH',
            'COUNT_MISMATCH',
            'WEIGHT_MISMATCH',
            'MISSING_OUTCOME',
            'UNALLOCATED_WEIGHT'
        )
    ),
    INDEX idx_anomalies_batch (batch_id, anomaly_code),
    INDEX idx_anomalies_code_detected (anomaly_code, detected_at)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;
-- Rollback only before business writes; see docs/processing-storage.md.
--rollback DROP TABLE batch_anomalies;
