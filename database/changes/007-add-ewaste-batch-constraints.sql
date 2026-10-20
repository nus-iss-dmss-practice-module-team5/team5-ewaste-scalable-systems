--liquibase formatted sql

--changeset team5:EWCSB1-007 dbms:mysql
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:1 SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'ewaste_batches';
--comment: DESIGN DRAFT. One ALTER adds batch row-level validation checks.
-- Existing incompatible rows cause this ALTER to fail; do not mark it ran.
ALTER TABLE ewaste_batches
    ADD CONSTRAINT ck_batches_status CHECK (
        status IN (
            'DRAFT', 'SUBMITTED', 'MATCHED', 'APPROVED', 'ASSIGNED',
            'COLLECTED', 'VERIFIED', 'RECYCLED', 'COMPLETED',
            'FAILED_COLLECTION'
        )
    ),
    ADD CONSTRAINT ck_batches_category CHECK (
        category IS NULL OR category IN (
            'ICT_EQUIPMENT', 'LARGE_APPLIANCE', 'BATTERIES',
            'CONSUMER_ELECTRONICS'
        )
    ),
    ADD CONSTRAINT ck_batches_quantity CHECK (
        quantity IS NULL OR quantity BETWEEN 1 AND 100000
    ),
    ADD CONSTRAINT ck_batches_weight CHECK (
        estimated_weight_kg IS NULL
        OR estimated_weight_kg BETWEEN 0.10 AND 50000.00
    ),
    ADD CONSTRAINT ck_batches_condition CHECK (
        condition_rating IS NULL
        OR condition_rating IN ('FUNCTIONAL', 'REPAIRABLE', 'END_OF_LIFE')
    ),
    ADD CONSTRAINT ck_batches_data_bearing CHECK (is_data_bearing IN (0, 1)),
    ADD CONSTRAINT ck_batches_zone CHECK (
        zone IS NULL OR zone IN ('NORTH', 'SOUTH', 'EAST', 'WEST', 'CENTRAL')
    ),
    ADD CONSTRAINT ck_batches_epoch CHECK (claim_epoch >= 1),
    ADD CONSTRAINT ck_batches_version CHECK (version >= 1),
    ADD CONSTRAINT ck_batches_updated_time CHECK (updated_at >= created_at),
    ADD CONSTRAINT ck_batches_submitted_time CHECK (
        submitted_at IS NULL OR submitted_at >= created_at
    ),
    ADD CONSTRAINT ck_batches_submit_completeness CHECK (
        (status = 'DRAFT' AND submitted_at IS NULL)
        OR (
            status <> 'DRAFT' AND submitted_at IS NOT NULL
            AND category IS NOT NULL AND quantity IS NOT NULL
            AND estimated_weight_kg IS NOT NULL
            AND condition_rating IS NOT NULL AND zone IS NOT NULL
            AND collection_deadline IS NOT NULL
            AND (submitted_at + INTERVAL 48 HOUR) IS NOT NULL
            AND (submitted_at + INTERVAL 90 DAY) IS NOT NULL
            AND collection_deadline >= submitted_at + INTERVAL 48 HOUR
            AND collection_deadline <= submitted_at + INTERVAL 90 DAY
        )
    );

-- API responsibilities: explicit submit boolean; reject excess decimals and
-- fractional counts before coercion; authenticated scope; DRAFT-only edits;
-- expected-version guard; one version increment; immutable submitted inputs.
-- A row CHECK does not prove an allowed transition or prior-value immutability.

-- PRE-WRITE rollback only; application writers must be disabled throughout.
-- These rollback lines form ONE ALTER TABLE statement, not separate statements.
--rollback ALTER TABLE ewaste_batches
--rollback     DROP CHECK ck_batches_submit_completeness,
--rollback     DROP CHECK ck_batches_submitted_time,
--rollback     DROP CHECK ck_batches_updated_time,
--rollback     DROP CHECK ck_batches_version,
--rollback     DROP CHECK ck_batches_epoch,
--rollback     DROP CHECK ck_batches_zone,
--rollback     DROP CHECK ck_batches_data_bearing,
--rollback     DROP CHECK ck_batches_condition,
--rollback     DROP CHECK ck_batches_weight,
--rollback     DROP CHECK ck_batches_quantity,
--rollback     DROP CHECK ck_batches_category,
--rollback     DROP CHECK ck_batches_status;
