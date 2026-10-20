--liquibase formatted sql

--changeset team5:EWCSB1-010 dbms:mysql
--comment: DESIGN DRAFT. Durable event intent in the batch transaction.
CREATE TABLE event_outbox (
    event_id              VARCHAR(36) NOT NULL,
    batch_id              VARCHAR(36) NOT NULL,
    command_id            VARCHAR(36) NOT NULL,
    event_type            VARCHAR(48) COLLATE utf8mb4_0900_as_cs NOT NULL,
    topic                 VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    schema_version        INT UNSIGNED NOT NULL,
    aggregate_version     INT UNSIGNED NOT NULL,
    sequence_in_command   INT UNSIGNED NOT NULL,
    partition_key         VARCHAR(36) NOT NULL,
    payload_json          JSON NOT NULL,
    correlation_id        VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    occurred_at           DATETIME(6) NOT NULL,
    created_at            DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    publish_state         VARCHAR(16) COLLATE utf8mb4_0900_as_cs
                              NOT NULL DEFAULT 'PENDING',
    attempt_count         INT UNSIGNED NOT NULL DEFAULT 0,
    next_attempt_at       DATETIME(6) NULL,
    published_at          DATETIME(6) NULL,
    last_error_code       VARCHAR(64) COLLATE utf8mb4_0900_as_cs NULL,

    CONSTRAINT pk_event_outbox PRIMARY KEY (event_id),
    CONSTRAINT uq_outbox_command_event
        UNIQUE (command_id, batch_id, event_type),
    CONSTRAINT uq_outbox_command_seq
        UNIQUE (command_id, sequence_in_command),
    CONSTRAINT fk_outbox_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_outbox_command
        FOREIGN KEY (command_id) REFERENCES command_idempotency (id),

    CONSTRAINT ck_outbox_schema_version CHECK (schema_version >= 1),
    CONSTRAINT ck_outbox_aggregate_version CHECK (aggregate_version >= 1),
    CONSTRAINT ck_outbox_sequence CHECK (sequence_in_command >= 1),
    CONSTRAINT ck_outbox_attempts CHECK (attempt_count >= 0),
    CONSTRAINT ck_outbox_key CHECK (partition_key = batch_id),
    CONSTRAINT ck_outbox_state CHECK (
        publish_state IN ('PENDING', 'PUBLISHED', 'QUARANTINED')
    ),
    CONSTRAINT ck_outbox_delivery_metadata CHECK (
        (publish_state = 'PENDING'
         AND next_attempt_at IS NOT NULL AND published_at IS NULL)
        OR (publish_state = 'PUBLISHED'
            AND published_at IS NOT NULL AND next_attempt_at IS NULL)
        OR (publish_state = 'QUARANTINED'
            AND published_at IS NULL AND next_attempt_at IS NULL)
    ),

    INDEX idx_outbox_due
        (publish_state, next_attempt_at, created_at, event_id),
    INDEX idx_outbox_batch
        (batch_id, aggregate_version, sequence_in_command, event_id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- DESTRUCTIVE ROLLBACK: pre-business-writes only, never erase published history.
--rollback DROP TABLE event_outbox;
