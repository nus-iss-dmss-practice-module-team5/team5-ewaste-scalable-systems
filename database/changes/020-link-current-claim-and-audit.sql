--liquibase formatted sql
--changeset team5:EWCSB3-020a dbms:mysql
--comment: DESIGN DRAFT. Replace only the C1 claim-pointer staging check.
ALTER TABLE ewaste_batches
    DROP CHECK ck_batches_c1_claim_null,
    ADD INDEX idx_batches_current_claim
        (current_claim_id, id, claim_epoch),
    ADD CONSTRAINT fk_batches_current_claim
        FOREIGN KEY (current_claim_id, id, claim_epoch)
        REFERENCES batch_claims (id, batch_id, claim_epoch),
    ADD CONSTRAINT ck_batches_claim_state CHECK (
        (status IN ('DRAFT', 'SUBMITTED', 'MATCHED')
         AND current_claim_id IS NULL)
        OR (status IN ('APPROVED', 'ASSIGNED', 'COLLECTED', 'VERIFIED',
                       'RECYCLED', 'COMPLETED', 'FAILED_COLLECTION')
            AND current_claim_id IS NOT NULL));
-- Keep ck_batches_c1_assignment_null for the later C4 migration.
-- Nullable pointer permits INSERT claim before UPDATE batch in one tx.
-- API also verifies claim_status ACCEPTED and full reservation consistency.
-- DESTRUCTIVE rollback: empty-data gates; no C4/later dependants present.
--rollback ALTER TABLE ewaste_batches
--rollback     DROP CHECK ck_batches_claim_state,
--rollback     DROP FOREIGN KEY fk_batches_current_claim,
--rollback     DROP INDEX idx_batches_current_claim,
--rollback     ADD CONSTRAINT ck_batches_c1_claim_null
--rollback         CHECK (current_claim_id IS NULL);

--changeset team5:EWCSB3-020b dbms:mysql
--comment: DESIGN DRAFT. Same-batch claim linkage for append-only audit.
ALTER TABLE batch_audit_events
    DROP CHECK ck_batch_audit_c1_claim_null,
    ADD INDEX idx_batch_audit_claim_batch (claim_id, batch_id),
    ADD CONSTRAINT fk_batch_audit_claim_batch
        FOREIGN KEY (claim_id, batch_id)
        REFERENCES batch_claims (id, batch_id);
-- Keep ck_batch_audit_c1_assignment_null until C4.
-- Apply both 020 changesets before enabling any C3 writers.
--rollback ALTER TABLE batch_audit_events
--rollback     DROP FOREIGN KEY fk_batch_audit_claim_batch,
--rollback     DROP INDEX idx_batch_audit_claim_batch,
--rollback     ADD CONSTRAINT ck_batch_audit_c1_claim_null
--rollback         CHECK (claim_id IS NULL);
