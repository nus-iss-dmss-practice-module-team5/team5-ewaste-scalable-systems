--liquibase formatted sql

--changeset team5:EWCSB1-009 dbms:mysql
--comment: DESIGN DRAFT. Shared append-only business lifecycle audit records.
CREATE TABLE batch_audit_events (
    id                    VARCHAR(36) NOT NULL,
    batch_id              VARCHAR(36) NOT NULL,
    command_id            VARCHAR(36) NOT NULL,
    assignment_id         VARCHAR(36) NULL,
    claim_id              VARCHAR(36) NULL,
    actor_user_id         VARCHAR(32) NULL,
    actor_org_id          VARCHAR(32) NULL,
    service_principal     VARCHAR(128) COLLATE utf8mb4_0900_as_cs NULL,
    event_type            VARCHAR(48) COLLATE utf8mb4_0900_as_cs NOT NULL,
    from_status           VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    to_status             VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    batch_version         INT UNSIGNED NOT NULL,
    sequence_in_command   INT UNSIGNED NOT NULL,
    occurred_at           DATETIME(6) NOT NULL,
    correlation_id        VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    details_json          JSON NOT NULL,

    CONSTRAINT pk_batch_audit_events PRIMARY KEY (id),
    CONSTRAINT uq_batch_audit_command_seq
        UNIQUE (command_id, sequence_in_command),
    CONSTRAINT fk_batch_audit_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_batch_audit_command
        FOREIGN KEY (command_id) REFERENCES command_idempotency (id),
    CONSTRAINT fk_batch_audit_actor
        FOREIGN KEY (actor_user_id) REFERENCES users (user_id),
    CONSTRAINT fk_batch_audit_org
        FOREIGN KEY (actor_org_id) REFERENCES organisations (organisation_id),

    CONSTRAINT ck_batch_audit_actor_mode CHECK (
        (actor_user_id IS NOT NULL AND actor_org_id IS NOT NULL
         AND service_principal IS NULL)
        OR (actor_user_id IS NULL AND actor_org_id IS NULL
            AND service_principal IS NOT NULL)
    ),
    CONSTRAINT ck_batch_audit_from_state CHECK (
        from_status IN (
            'DRAFT', 'SUBMITTED', 'MATCHED', 'APPROVED', 'ASSIGNED',
            'COLLECTED', 'VERIFIED', 'RECYCLED', 'COMPLETED',
            'FAILED_COLLECTION'
        )
    ),
    CONSTRAINT ck_batch_audit_to_state CHECK (
        to_status IN (
            'DRAFT', 'SUBMITTED', 'MATCHED', 'APPROVED', 'ASSIGNED',
            'COLLECTED', 'VERIFIED', 'RECYCLED', 'COMPLETED',
            'FAILED_COLLECTION'
        )
    ),
    CONSTRAINT ck_batch_audit_version CHECK (batch_version >= 1),
    CONSTRAINT ck_batch_audit_sequence CHECK (sequence_in_command >= 1),
    CONSTRAINT ck_batch_audit_c1_claim_null CHECK (claim_id IS NULL),
    CONSTRAINT ck_batch_audit_c1_assignment_null CHECK (assignment_id IS NULL),

    INDEX idx_batch_audit_history
        (batch_id, batch_version, sequence_in_command, id),
    INDEX idx_batch_audit_actor (actor_user_id),
    INDEX idx_batch_audit_org (actor_org_id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- Append-only is enforced by runtime privileges and command implementation.
-- Scalar FKs prove existence; commit guards prove command/batch/actor agreement.
-- event_type is extensible. C1 values: DraftSaved and RequestSubmitted.
-- Draft create: DRAFT -> DRAFT, details_json.operation=CREATE (not prior history).
-- DESTRUCTIVE ROLLBACK: disposable/pre-business-write databases only.
--rollback DROP TABLE batch_audit_events;
