--liquibase formatted sql

--changeset team5:EWCSB1-008 dbms:mysql
--comment: DESIGN DRAFT. Shared durable actor/command-scoped replay records.
CREATE TABLE command_idempotency (
    id                  VARCHAR(36) NOT NULL,
    actor_user_id       VARCHAR(32) NULL,
    service_principal   VARCHAR(128) COLLATE utf8mb4_0900_as_cs NULL,
    actor_scope         VARCHAR(160) COLLATE utf8mb4_0900_as_cs NOT NULL,
    command_name        VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    idempotency_key     VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    request_hash        CHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    batch_id            VARCHAR(36) NULL,
    assignment_id       VARCHAR(36) NULL,
    state               VARCHAR(16) COLLATE utf8mb4_0900_as_cs
                            NOT NULL DEFAULT 'IN_PROGRESS',
    response_status     SMALLINT NULL,
    response_json       JSON NULL,
    created_at          DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    completed_at        DATETIME(6) NULL,
    retain_until        DATETIME(6) NOT NULL,

    CONSTRAINT pk_command_idempotency PRIMARY KEY (id),
    CONSTRAINT uq_command_replay
        UNIQUE (actor_scope, command_name, idempotency_key),
    CONSTRAINT fk_command_actor
        FOREIGN KEY (actor_user_id) REFERENCES users (user_id),
    CONSTRAINT fk_command_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),

    CONSTRAINT ck_command_actor_mode CHECK (
        (actor_user_id IS NOT NULL AND service_principal IS NULL)
        OR (actor_user_id IS NULL AND service_principal IS NOT NULL)
    ),
    CONSTRAINT ck_command_state CHECK (state IN ('IN_PROGRESS', 'COMPLETED')),
    CONSTRAINT ck_command_completion CHECK (
        (state = 'IN_PROGRESS'
         AND response_status IS NULL AND completed_at IS NULL)
        OR (state = 'COMPLETED'
            AND response_status IS NOT NULL AND response_json IS NOT NULL
            AND completed_at IS NOT NULL AND completed_at >= created_at
            AND retain_until >= completed_at)
    ),
    CONSTRAINT ck_command_retention CHECK (retain_until >= created_at),
    CONSTRAINT ck_command_c1_assignment_null CHECK (assignment_id IS NULL),

    INDEX idx_command_batch (batch_id),
    INDEX idx_command_actor (actor_user_id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- C1 success requires COMPLETED plus batch_id in the same transaction.
-- The API verifies canonical actor_scope, SHA-256 request_hash and replay scope.
-- command_name is extensible; there is no global UNIQUE(idempotency_key) here.
-- Retention expiry does not permit deleting identities referenced by history.
-- DESTRUCTIVE ROLLBACK: pre-business-writes only; first roll back 010 and 009.
--rollback DROP TABLE command_idempotency;
