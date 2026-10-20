-- Test-only authentication schema for the Docker Compose stack.
-- Production and shared environments must use the approved Liquibase migrations.

CREATE TABLE IF NOT EXISTS organisations (
    organisation_id   VARCHAR(32)  NOT NULL,
    organisation_name VARCHAR(160) NOT NULL,
    organisation_type VARCHAR(40)  NOT NULL,
    status            VARCHAR(24)  NOT NULL,
    created_at        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (organisation_id),
    UNIQUE KEY uq_organisations_name (organisation_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS roles (
    role_code                 VARCHAR(32)  NOT NULL,
    display_name              VARCHAR(80)  NOT NULL,
    description               VARCHAR(255) NULL,
    allowed_organisation_type VARCHAR(40)  NULL,
    is_active                 BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at                DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (role_code),
    UNIQUE KEY uq_roles_display_name (display_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS users (
    user_id              VARCHAR(32)  NOT NULL,
    email                VARCHAR(254) NOT NULL,
    display_name         VARCHAR(120) NOT NULL,
    password_hash        VARCHAR(255) NOT NULL,
    role_code            VARCHAR(32)  NOT NULL,
    organisation_id      VARCHAR(32)  NOT NULL,
    status               VARCHAR(24)  NOT NULL,
    failed_login_attempts SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    locked_until         DATETIME(3)  NULL,
    last_login_at        DATETIME(3)  NULL,
    created_at           DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at           DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id),
    UNIQUE KEY uq_users_email (email),
    KEY ix_users_role (role_code),
    KEY ix_users_organisation (organisation_id),
    CONSTRAINT fk_users_role FOREIGN KEY (role_code) REFERENCES roles (role_code),
    CONSTRAINT fk_users_organisation FOREIGN KEY (organisation_id) REFERENCES organisations (organisation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS sessions (
    session_id          CHAR(36)    NOT NULL,
    user_id             VARCHAR(32) NOT NULL,
    refresh_token_hash  CHAR(64)    NOT NULL,
    status              VARCHAR(16) NOT NULL,
    issued_at           DATETIME(3) NOT NULL,
    expires_at          DATETIME(3) NOT NULL,
    last_seen_at        DATETIME(3) NULL,
    revoked_at          DATETIME(3) NULL,
    revocation_reason   VARCHAR(120) NULL,
    PRIMARY KEY (session_id),
    KEY ix_sessions_user (user_id),
    KEY ix_sessions_expiry (expires_at),
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO organisations (organisation_id, organisation_name, organisation_type, status)
VALUES
    ('PLATFORM', 'E-Waste Platform', 'PLATFORM', 'ACTIVE'),
    ('DON-001', 'Green Office', 'DONOR', 'ACTIVE'),
    ('DON-002', 'Community Hub', 'DONOR', 'ACTIVE'),
    ('COL-001', 'Green Collect', 'COLLECTOR', 'ACTIVE'),
    ('COL-002', 'EcoPickup', 'COLLECTOR', 'ACTIVE'),
    ('PROC-001', 'EcoCycle', 'RECYCLER', 'ACTIVE'),
    ('PROC-002', 'RenewTech', 'RECYCLER', 'ACTIVE')
ON DUPLICATE KEY UPDATE organisation_name = VALUES(organisation_name);

INSERT INTO roles (role_code, display_name, description, allowed_organisation_type)
VALUES
    ('SYSTEM_ADMIN', 'System Administrator', 'Full platform administration', 'PLATFORM'),
    ('AUDITOR', 'Auditor', 'Read-only audit access', 'PLATFORM'),
    ('DONOR', 'Donor', 'Creates and manages donations', 'DONOR'),
    ('COLLECTOR', 'Collector', 'Manages collection operations', 'COLLECTOR'),
    ('RECYCLER', 'Recycler', 'Manages recycling operations', 'RECYCLER')
ON DUPLICATE KEY UPDATE display_name = VALUES(display_name);
