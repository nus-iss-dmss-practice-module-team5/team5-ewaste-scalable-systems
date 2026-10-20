--liquibase formatted sql
--changeset team5:EWCSB4-022 dbms:mysql
--comment: DESIGN DRAFT. One retained identity for each collector attempt.
CREATE TABLE batch_assignments (
    id VARCHAR(36) NOT NULL,
    batch_id VARCHAR(36) NOT NULL,
    claim_id VARCHAR(36) NOT NULL,
    recycler_org_id VARCHAR(32) NOT NULL,
    collector_org_id VARCHAR(32) NOT NULL,
    collector_user_id VARCHAR(32) NOT NULL,
    collector_scope_id VARCHAR(36) NOT NULL,
    assignment_sequence BIGINT UNSIGNED NOT NULL,
    claim_epoch BIGINT UNSIGNED NOT NULL,
    active_batch_id VARCHAR(36) GENERATED ALWAYS AS (
        CASE WHEN closed_at IS NULL THEN batch_id ELSE NULL END) STORED,
    previous_assignment_id VARCHAR(36) NULL,
    assignment_status VARCHAR(24) COLLATE utf8mb4_0900_as_cs
        NOT NULL DEFAULT 'ACCEPTED',
    rejection_reason VARCHAR(255) NULL,
    reassignment_reason VARCHAR(255) NULL,
    assigned_at DATETIME(6) NOT NULL,
    responded_at DATETIME(6) NULL,
    closed_at DATETIME(6) NULL,
    closure_reason VARCHAR(48) NULL,
    version BIGINT NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT pk_batch_assignments PRIMARY KEY (id),
    CONSTRAINT uq_assignment_sequence UNIQUE (batch_id, assignment_sequence),
    CONSTRAINT uq_assignment_open UNIQUE (active_batch_id),
    CONSTRAINT uq_assignment_id_batch UNIQUE (id, batch_id),
    CONSTRAINT uq_assignment_current_tuple
        UNIQUE (id, batch_id, claim_id, claim_epoch),
    CONSTRAINT uq_assignment_actor_tuple
        UNIQUE (id, batch_id, collector_user_id, collector_org_id),
    CONSTRAINT fk_assignment_batch
        FOREIGN KEY (batch_id) REFERENCES ewaste_batches (id),
    CONSTRAINT fk_assignment_claim_epoch
        FOREIGN KEY (claim_id, batch_id, claim_epoch)
        REFERENCES batch_claims (id, batch_id, claim_epoch),
    CONSTRAINT fk_assignment_scope_pair
        FOREIGN KEY (collector_scope_id, recycler_org_id, collector_org_id)
        REFERENCES recycler_collector_scopes
            (id, recycler_org_id, collector_org_id),
    CONSTRAINT fk_assignment_collector
        FOREIGN KEY (collector_user_id) REFERENCES users (user_id),
    CONSTRAINT fk_assignment_previous_batch
        FOREIGN KEY (previous_assignment_id, batch_id)
        REFERENCES batch_assignments (id, batch_id),
    CONSTRAINT ck_assignment_sequence CHECK (assignment_sequence >= 1),
    CONSTRAINT ck_assignment_epoch CHECK (claim_epoch >= 1),
    CONSTRAINT ck_assignment_version CHECK (version >= 1),
    CONSTRAINT ck_assignment_status CHECK (
        assignment_status IN ('PENDING', 'ACCEPTED', 'SUPERSEDED',
                              'COMPLETED', 'FAILED')),
    CONSTRAINT ck_assignment_closure CHECK (
        (assignment_status IN ('PENDING', 'ACCEPTED')
         AND closed_at IS NULL AND closure_reason IS NULL)
        OR (assignment_status IN ('SUPERSEDED', 'COMPLETED', 'FAILED')
            AND closed_at IS NOT NULL AND closed_at >= assigned_at
            AND closure_reason IS NOT NULL
            AND CHAR_LENGTH(TRIM(closure_reason)) BETWEEN 1 AND 48)),
    CONSTRAINT ck_assignment_response CHECK (
        (assignment_status = 'PENDING' AND responded_at IS NULL)
        OR (assignment_status = 'ACCEPTED' AND responded_at IS NOT NULL
            AND responded_at >= assigned_at)
        OR (assignment_status IN ('SUPERSEDED', 'COMPLETED', 'FAILED')
            AND (responded_at IS NULL OR responded_at >= assigned_at))),
    CONSTRAINT ck_assignment_response_before_close CHECK (
        responded_at IS NULL OR closed_at IS NULL
        OR responded_at <= closed_at),
    CONSTRAINT ck_assignment_rejection_reason CHECK (
        rejection_reason IS NULL
        OR CHAR_LENGTH(TRIM(rejection_reason)) BETWEEN 1 AND 255),
    CONSTRAINT ck_assignment_predecessor CHECK (
        (assignment_sequence = 1 AND previous_assignment_id IS NULL
         AND reassignment_reason IS NULL)
        OR (assignment_sequence > 1 AND previous_assignment_id IS NOT NULL
            AND previous_assignment_id <> id
            AND reassignment_reason IS NOT NULL
            AND CHAR_LENGTH(TRIM(reassignment_reason)) BETWEEN 1 AND 255)),
    CONSTRAINT ck_assignment_created CHECK (created_at >= assigned_at),
    CONSTRAINT ck_assignment_updated CHECK (updated_at >= created_at),
    CONSTRAINT ck_assignment_response_updated CHECK (
        responded_at IS NULL OR updated_at >= responded_at),
    CONSTRAINT ck_assignment_close_updated CHECK (
        closed_at IS NULL OR updated_at >= closed_at),
    INDEX idx_assignment_claim_epoch (claim_id, batch_id, claim_epoch),
    INDEX idx_assignment_scope_pair
        (collector_scope_id, recycler_org_id, collector_org_id),
    INDEX idx_assignment_previous (previous_assignment_id, batch_id),
    INDEX idx_assignment_collector_tasks
        (collector_user_id, closed_at, assigned_at, id),
    INDEX idx_assignment_collector_org (collector_org_id, assigned_at, id),
    INDEX idx_assignment_recycler (recycler_org_id, assigned_at, id)
) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4
  COLLATE=utf8mb4_0900_ai_ci;
-- New choice: ACCEPTED and responded_at=assigned_at; version=1.
-- PENDING is compatibility only. Closed legacy-origin PENDING rows may keep
-- responded_at NULL; do not invent an acceptance timestamp.
-- API locks batch first; checks current ACCEPTED claim, same recycler,
-- active actor/scope, reservation, and current expected versions.
-- Replacement: immediate prior same-batch row, sequence +1, different user.
-- Keep collector/claim/scope identity immutable; never reopen closed rows.
-- Reject before pickup: SUPERSEDED with reason; batch returns APPROVED.
-- Failed pickup: FAILED remains unchanged when recovery clears the pointer.
-- Generated active_batch_id is not an input or an FK target.
-- DESTRUCTIVE rollback only after gates in ../migration-order.md pass.
--rollback DROP TABLE batch_assignments;
