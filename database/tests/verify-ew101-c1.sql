-- EW-101-C1 database verification queries.
-- The test runner treats any row whose result is FAIL as a failed test.

SELECT test_name, result, actual, expected
FROM (
    SELECT
        'schema.organisations_table' AS test_name,
        IF(COUNT(*) = 1, 'PASS', 'FAIL') AS result,
        CAST(COUNT(*) AS CHAR) AS actual,
        '1' AS expected
    FROM information_schema.tables
    WHERE table_schema = DATABASE() AND table_name = 'organisations'

    UNION ALL

    SELECT
        'schema.roles_table',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.tables
    WHERE table_schema = DATABASE() AND table_name = 'roles'

    UNION ALL

    SELECT
        'schema.users_table',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.tables
    WHERE table_schema = DATABASE() AND table_name = 'users'

    UNION ALL

    SELECT
        'schema.sessions_table',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.tables
    WHERE table_schema = DATABASE() AND table_name = 'sessions'

    UNION ALL

    SELECT
        'schema.login_audit_table',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.tables
    WHERE table_schema = DATABASE()
        AND table_name = 'login_audit'

    UNION ALL

    SELECT
        'seed.organisation_count',
        IF(COUNT(*) = 7, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '7'
    FROM organisations

    UNION ALL

    SELECT
        'seed.role_count',
        IF(COUNT(*) = 5, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '5'
    FROM roles

    UNION ALL

    SELECT
        'seed.user_count',
        IF(COUNT(*) = 9, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '9'
    FROM users

    UNION ALL

    SELECT
        'seed.sessions_initially_empty',
        IF(COUNT(*) = 0, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '0'
    FROM sessions

    UNION ALL

    SELECT
        'seed.login_audit_initially_empty',
        IF(COUNT(*) = 0, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '0'
    FROM login_audit

    UNION ALL

    SELECT
        'integrity.no_orphan_users',
        IF(COUNT(*) = 0, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '0'
    FROM users u
    LEFT JOIN organisations o ON o.organisation_id = u.organisation_id
    LEFT JOIN roles r ON r.role_code = u.role_code
    WHERE o.organisation_id IS NULL OR r.role_code IS NULL

    UNION ALL

    SELECT
        'integrity.role_matches_organisation_type',
        IF(COUNT(*) = 0, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '0'
    FROM users u
    JOIN organisations o ON o.organisation_id = u.organisation_id
    JOIN roles r ON r.role_code = u.role_code
    WHERE o.organisation_type <> r.allowed_organisation_type

    UNION ALL

    SELECT
        'hierarchy.platform_admin_and_auditor',
        IF(COUNT(*) = 2, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '2'
    FROM users
    WHERE organisation_id = 'PLATFORM'
      AND role_code IN ('SYSTEM_ADMIN', 'AUDITOR')
      AND status = 'ACTIVE'

    UNION ALL

    SELECT
        'hierarchy.collection_operators',
        IF(COUNT(*) = 2, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '2'
    FROM users
    WHERE role_code = 'COLLECTOR'
      AND organisation_id IN ('COL-001', 'COL-002')
      AND status = 'ACTIVE'

    UNION ALL

    SELECT
        'hierarchy.recyclers',
        IF(COUNT(*) = 2, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '2'
    FROM users
    WHERE role_code = 'RECYCLER'
      AND organisation_id IN ('PROC-001', 'PROC-002')
      AND status = 'ACTIVE'

    UNION ALL

    SELECT
        'negative.disabled_user',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM users
    WHERE email = 'disabled@ewaste.test'
      AND status = 'DISABLED'

    UNION ALL

    SELECT
        'security.passwords_are_bcrypt_hashes',
        IF(COUNT(*) = 9, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '9'
    FROM users
    WHERE CHAR_LENGTH(password_hash) = 60
      AND LEFT(password_hash, 4) IN ('$2a$', '$2b$', '$2y$')

    UNION ALL

    SELECT
        'security.no_plaintext_seed_password',
        IF(COUNT(*) = 0, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '0'
    FROM users
    WHERE password_hash = 'TestOnly#2026!'

    UNION ALL

    SELECT
        'security.token_hash_column_present',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'sessions'
      AND column_name = 'token_hash'
      AND data_type = 'char'
      AND character_maximum_length = 64
      AND is_nullable = 'NO'

    UNION ALL

    SELECT
        'security.no_raw_token_storage',
        IF(COUNT(*) = 0, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '0'
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'sessions'
      AND column_name IN (
          'token',
          'jwt',
          'jwt_token',
          'access_token',
          'refresh_token',
          'access_jwt',
          'refresh_jwt'
      )

    UNION ALL

    SELECT
        'constraint.unique_session_token_hash',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.statistics
    WHERE table_schema = DATABASE()
      AND table_name = 'sessions'
      AND index_name = 'uq_sessions_token_hash'
      AND non_unique = 0

    UNION ALL

    SELECT
        'constraint.unique_user_email',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.statistics
    WHERE table_schema = DATABASE()
      AND table_name = 'users'
      AND index_name = 'uq_users_email'
      AND non_unique = 0

    UNION ALL

    SELECT
        'constraint.expected_foreign_keys',
        IF(COUNT(*) = 4, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '4'
    FROM information_schema.referential_constraints
    WHERE constraint_schema = DATABASE()
      AND constraint_name IN (
          'fk_users_role',
          'fk_users_organisation',
          'fk_sessions_user',
          'fk_login_audit_user'
      )

    UNION ALL

    SELECT
        'session.primary_key_supports_jwt_jti',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
        AND table_name = 'sessions'
        AND column_name = 'session_id'
        AND character_maximum_length = 36

    UNION ALL

    SELECT
        'audit.correlation_id_present',
        IF(COUNT(*) = 1, 'PASS', 'FAIL'),
        CAST(COUNT(*) AS CHAR),
        '1'
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
        AND table_name = 'login_audit'
        AND column_name = 'correlation_id'
) checks
ORDER BY test_name;
