--liquibase formatted sql

--changeset team5:EW101-C1-102 dbms:mysql context:@seed labels:synthetic-test-data
--comment: Seed the five Sprint 1 roles and their expected organisation ownership type.
INSERT INTO roles (
    role_code,
    display_name,
    description,
    allowed_organisation_type,
    is_active
) VALUES
    ('SYSTEM_ADMIN', 'System Administrator', 'Manages platform users, organisations, controls, and security settings.', 'PLATFORM', 1),
    ('AUDITOR',      'Auditor',              'Reviews chain-of-custody, evidence, anomalies, and audit records without modifying operations.', 'PLATFORM', 1),
    ('DONOR',        'Donor',                'Creates and tracks e-waste batches and collection requests for its donor organisation.', 'DONOR', 1),
    ('COLLECTOR',    'Collector',            'Accepts collection assignments and records collection and handoff activities.', 'COLLECTION_OPERATOR', 1),
    ('RECYCLER',     'Recycler',             'Verifies received e-waste, records processing details and evidence, and records the final reuse, recycling, or disposal outcome.', 'PROCESSING_FACILITY', 1);

--rollback DELETE FROM roles WHERE role_code IN ('SYSTEM_ADMIN', 'AUDITOR', 'DONOR', 'COLLECTOR', 'RECYCLER');
