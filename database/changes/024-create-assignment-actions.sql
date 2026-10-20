--liquibase formatted sql
--changeset team5:EWCSB4-024 dbms:mysql
--comment: DESIGN DRAFT. Append-only assignment action snapshots, not state.
CREATE TABLE assignment_actions (
    id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    assignment_id VARCHAR(36) NOT NULL,
    action_type VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    actor_user_id VARCHAR(32) NULL,
    service_principal VARCHAR(128) COLLATE utf8mb4_0900_as_cs NULL,
    reason VARCHAR(255) NULL,
    previous_assignment_id VARCHAR(36) NULL,
    from_status VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    to_status VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    assignment_version BIGINT NOT NULL,
    command_id VARCHAR(36) NOT NULL,
    occurred_at DATETIME(6) NOT NULL,
    correlation_id VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    details_json JSON NOT NULL,
    CONSTRAINT pk_assignment_actions PRIMARY KEY (id),
    CONSTRAINT uq_assignment_action_command
        UNIQUE (command_id, assignment_id, action_type),
    CONSTRAINT fk_assignment_action_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_assignment_action_attempt
        FOREIGN KEY (assignment_id, batch_id)
        REFERENCES batch_assignments (id, batch_id),
    CONSTRAINT fk_assignment_action_previous
        FOREIGN KEY (previous_assignment_id, batch_id)
        REFERENCES batch_assignments (id, batch_id),
    CONSTRAINT fk_assignment_action_actor
        FOREIGN KEY (actor_user_id) REFERENCES users (user_id),
    CONSTRAINT fk_assignment_action_command
        FOREIGN KEY (command_id) REFERENCES command_idempotency (id),
    CONSTRAINT ck_assignment_action_type CHECK (
        action_type IN ('ASSIGNED', 'ACCEPTED', 'REJECTED',
            'HANDOFF_RECORDED', 'PICKUP_FAILED', 'SUPERSEDED', 'REASSIGNED')),
    CONSTRAINT ck_assignment_action_actor CHECK (
        (actor_user_id IS NOT NULL AND service_principal IS NULL)
        OR (actor_user_id IS NULL AND service_principal IS NOT NULL
            AND CHAR_LENGTH(TRIM(service_principal)) > 0)),
    CONSTRAINT ck_assignment_action_reason CHECK (
        (reason IS NULL OR CHAR_LENGTH(TRIM(reason)) BETWEEN 1 AND 255)
        AND (action_type NOT IN ('REJECTED', 'SUPERSEDED', 'REASSIGNED')
             OR reason IS NOT NULL)),
    CONSTRAINT ck_assignment_action_previous CHECK (
        (previous_assignment_id IS NULL
         OR previous_assignment_id <> assignment_id)
        AND (action_type <> 'REASSIGNED'
             OR previous_assignment_id IS NOT NULL)),
    CONSTRAINT ck_assignment_action_from CHECK (
        from_status IN ('DRAFT', 'SUBMITTED', 'MATCHED', 'APPROVED',
            'ASSIGNED', 'COLLECTED', 'VERIFIED', 'RECYCLED', 'COMPLETED',
            'FAILED_COLLECTION')),
    CONSTRAINT ck_assignment_action_to CHECK (
        to_status IN ('DRAFT', 'SUBMITTED', 'MATCHED', 'APPROVED',
            'ASSIGNED', 'COLLECTED', 'VERIFIED', 'RECYCLED', 'COMPLETED',
            'FAILED_COLLECTION')),
    CONSTRAINT ck_assignment_action_version CHECK (assignment_version >= 1),
    INDEX idx_assignment_action_history
        (assignment_id, batch_id, assignment_version, occurred_at, id),
    INDEX idx_assignment_action_batch (batch_id, occurred_at, id),
    INDEX idx_assignment_action_previous (previous_assignment_id, batch_id),
    INDEX idx_assignment_action_actor (actor_user_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci;
-- from_status/to_status are immutable snapshots, not another current state.
-- API: action, actor, command, version and predecessor agree with the write.
-- Selection emits ASSIGNED; replacement also emits REASSIGNED.
-- Pre-pickup rejection emits REJECTED and retains SUPERSEDED assignment.
-- Automatic FAILED_COLLECTION->APPROVED recovery writes shared audit/command
-- only; no fictitious REASSIGNED action before a new collector chooses.
-- No UPDATE/DELETE history rights for ordinary application commands.
-- DESTRUCTIVE rollback only after gates in ../migration-order.md pass.
--rollback DROP TABLE assignment_actions;
