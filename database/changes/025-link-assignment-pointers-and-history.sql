--liquibase formatted sql
--changeset team5:EWCSB4-025a dbms:mysql
--comment: DESIGN DRAFT. Replace only the C1 assignment-pointer guard.
ALTER TABLE ewaste_batches
    DROP CHECK ck_batches_c1_assignment_null,
    ADD INDEX idx_batches_current_assignment
        (current_assignment_id, id, current_claim_id, claim_epoch),
    ADD INDEX idx_batches_failed_recovery (status, updated_at, id),
    ADD CONSTRAINT fk_batches_current_assignment
        FOREIGN KEY (current_assignment_id, id, current_claim_id, claim_epoch)
        REFERENCES batch_assignments (id, batch_id, claim_id, claim_epoch),
    ADD CONSTRAINT ck_batches_assignment_state CHECK (
        (status IN ('DRAFT', 'SUBMITTED', 'MATCHED', 'APPROVED')
         AND current_assignment_id IS NULL)
        OR (status IN ('ASSIGNED', 'COLLECTED', 'VERIFIED', 'RECYCLED',
                       'COMPLETED', 'FAILED_COLLECTION')
            AND current_assignment_id IS NOT NULL));
-- Keep C3 fk_batches_current_claim and ck_batches_claim_state unchanged.
-- ASSIGNED points to an open attempt; failure/success retain closed attempt
-- until failure recovery clears it. Row CHECK cannot prove parent status.
-- Insert assignment before setting pointer: MySQL FKs are immediate.
--rollback ALTER TABLE ewaste_batches
--rollback     DROP CHECK ck_batches_assignment_state,
--rollback     DROP FOREIGN KEY fk_batches_current_assignment,
--rollback     DROP INDEX idx_batches_current_assignment,
--rollback     DROP INDEX idx_batches_failed_recovery,
--rollback     ADD CONSTRAINT ck_batches_c1_assignment_null
--rollback         CHECK (current_assignment_id IS NULL);

--changeset team5:EWCSB4-025b dbms:mysql
--comment: DESIGN DRAFT. Same-batch assignment scope for durable commands.
ALTER TABLE command_idempotency
    DROP CHECK ck_command_c1_assignment_null,
    ADD INDEX idx_command_assignment_batch (assignment_id, batch_id),
    ADD CONSTRAINT fk_command_assignment_batch
        FOREIGN KEY (assignment_id, batch_id)
        REFERENCES batch_assignments (id, batch_id),
    ADD CONSTRAINT ck_command_assignment_batch CHECK (
        assignment_id IS NULL OR batch_id IS NOT NULL);
-- NULL pair bypass is blocked; actor/command/resource agreement is API-owned.
-- For a selection, create assignment before completing its command pointer.
--rollback ALTER TABLE command_idempotency
--rollback     DROP CHECK ck_command_assignment_batch,
--rollback     DROP FOREIGN KEY fk_command_assignment_batch,
--rollback     DROP INDEX idx_command_assignment_batch,
--rollback     ADD CONSTRAINT ck_command_c1_assignment_null
--rollback         CHECK (assignment_id IS NULL);

--changeset team5:EWCSB4-025c dbms:mysql
--comment: DESIGN DRAFT. Same-batch assignment reference in shared audit.
ALTER TABLE batch_audit_events
    DROP CHECK ck_batch_audit_c1_assignment_null,
    ADD INDEX idx_batch_audit_assignment_batch (assignment_id, batch_id),
    ADD CONSTRAINT fk_batch_audit_assignment_batch
        FOREIGN KEY (assignment_id, batch_id)
        REFERENCES batch_assignments (id, batch_id);
-- Keep C3 audit claim linkage unchanged. Enable C4 only after 025c succeeds.
-- All rollback comments require the empty-data gates in migration-order.md.
--rollback ALTER TABLE batch_audit_events
--rollback     DROP FOREIGN KEY fk_batch_audit_assignment_batch,
--rollback     DROP INDEX idx_batch_audit_assignment_batch,
--rollback     ADD CONSTRAINT ck_batch_audit_c1_assignment_null
--rollback         CHECK (assignment_id IS NULL);
