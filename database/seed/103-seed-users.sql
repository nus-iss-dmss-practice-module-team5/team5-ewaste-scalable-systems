--liquibase formatted sql

--changeset team5:EW101-C1-103 dbms:mysql context:@seed labels:synthetic-test-data
--comment: Seed synthetic users. Passwords are bcrypt hashes; no raw password is stored in the database.
INSERT INTO users (
    user_id,
    email,
    display_name,
    password_hash,
    role_code,
    organisation_id,
    status
) VALUES
    ('USR-001', 'admin@ewaste.test',      'Platform Administrator',            '$2y$12$JacrIti4eWDYBwHpv3qkkuWw.Z0ZyZDC6K4Nsg1Jm2ybAAKwZQuou', 'SYSTEM_ADMIN', 'PLATFORM', 'ACTIVE'),
    ('USR-002', 'auditor@ewaste.test',    'Platform Auditor',                  '$2y$12$1qBNM6nPjNz4i6wLlxWlKe/.eaeWjDa2aacs9.4ZS3K16UyKNjvg2', 'AUDITOR',      'PLATFORM', 'ACTIVE'),
    ('USR-003', 'donor1@ewaste.test',     'Green Office Donor',                '$2y$12$QRORxqqgcMOSA1PaN7ra8uXYKkl4w3Rw3Jb0r.faDQsIxm.0xUsXu', 'DONOR',        'DON-001',  'ACTIVE'),
    ('USR-004', 'donor2@ewaste.test',     'Community Hub Donor',               '$2y$12$kEhNbHyyXhN3eEqnqObKwO6GOGwruBnDwUiub0/3EM7J29eNgEM5m', 'DONOR',        'DON-002',  'ACTIVE'),
    ('USR-005', 'collector1@ewaste.test', 'Green Collect Operator',            '$2y$12$swBFMAYo3uJHFiIF7/I66uNAPH5VF.mc3/bCA51M0IDlyBBRh6fKq', 'COLLECTOR',    'COL-001',  'ACTIVE'),
    ('USR-006', 'collector2@ewaste.test', 'EcoPickup Operator',                '$2y$12$duWw0MfZrwr3lYh60sdr4ObgDNiDSfOTyYyfR/0j.RSozvC1YD.Aa', 'COLLECTOR',    'COL-002',  'ACTIVE'),
    ('USR-007', 'recycler1@ewaste.test',  'EcoCycle Processing Facility',      '$2y$12$ZQTi6WeHZc7R6DLaaal87u//T8iQP7.CknjoYT6vJ.mNVqEg/v7Qe', 'RECYCLER',     'PROC-001', 'ACTIVE'),
    ('USR-008', 'recycler2@ewaste.test',  'RenewTech Processing Facility',     '$2y$12$MnpX5gW4MQJ3H4WLnmV9FukOVu4TE29OSRBehPOQutWr8Khq3vZH.', 'RECYCLER',     'PROC-002', 'ACTIVE'),
    ('USR-009', 'disabled@ewaste.test',   'Disabled Donor',                    '$2y$12$wH3JIlFug3W5gbyW6hXYq.n1A9xSgBoj6Bjkld0pzazhw6cmGrZgi', 'DONOR',        'DON-001',  'DISABLED');

--rollback DELETE FROM users WHERE user_id IN ('USR-001', 'USR-002', 'USR-003', 'USR-004', 'USR-005', 'USR-006', 'USR-007', 'USR-008', 'USR-009');
