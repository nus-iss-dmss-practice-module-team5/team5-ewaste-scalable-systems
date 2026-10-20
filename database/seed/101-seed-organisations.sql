--liquibase formatted sql

--changeset team5:EW101-C1-101 dbms:mysql context:@seed labels:synthetic-test-data
--comment: Seed synthetic organisations used for Sprint 1 login and organisation-boundary tests.
INSERT INTO organisations (
    organisation_id,
    organisation_name,
    organisation_type,
    status
) VALUES
    ('PLATFORM', 'E-Waste Platform',              'PLATFORM',            'ACTIVE'),
    ('DON-001',  'Green Office SG',               'DONOR',               'ACTIVE'),
    ('DON-002',  'Community Hub SG',              'DONOR',               'ACTIVE'),
    ('COL-001',  'Green Collect SG',              'COLLECTION_OPERATOR', 'ACTIVE'),
    ('COL-002',  'EcoPickup SG',                  'COLLECTION_OPERATOR', 'ACTIVE'),
    ('PROC-001', 'EcoCycle Processing Facility',  'PROCESSING_FACILITY', 'ACTIVE'),
    ('PROC-002', 'RenewTech Processing Facility', 'PROCESSING_FACILITY', 'ACTIVE');

--rollback DELETE FROM organisations WHERE organisation_id IN ('PLATFORM', 'DON-001', 'DON-002', 'COL-001', 'COL-002', 'PROC-001', 'PROC-002');
