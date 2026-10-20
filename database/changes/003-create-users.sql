--liquibase formatted sql

--changeset team5:EW101-C1-003 dbms:mysql
--comment: Create users with one role and one owning organisation for Sprint 1 authentication and RBAC.
CREATE TABLE users (
    user_id                 VARCHAR(32)  NOT NULL,
    email                   VARCHAR(254) NOT NULL,
    display_name            VARCHAR(120) NOT NULL,
    password_hash           VARCHAR(255) NOT NULL,
    role_code               VARCHAR(32)  NOT NULL,
    organisation_id         VARCHAR(32)  NOT NULL,
    status                  VARCHAR(24)  NOT NULL DEFAULT 'PENDING',
    failed_login_attempts   SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    locked_until            TIMESTAMP(6) NULL,
    last_login_at           TIMESTAMP(6) NULL,
    created_at              TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at              TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                                            ON UPDATE CURRENT_TIMESTAMP(6),

    CONSTRAINT pk_users PRIMARY KEY (user_id),
    CONSTRAINT uq_users_email UNIQUE (email),
    CONSTRAINT fk_users_role
        FOREIGN KEY (role_code) REFERENCES roles (role_code)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_users_organisation
        FOREIGN KEY (organisation_id) REFERENCES organisations (organisation_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT ck_users_status CHECK (
        status IN ('PENDING', 'ACTIVE', 'DISABLED')
    ),

    INDEX idx_users_organisation (organisation_id),
    INDEX idx_users_role (role_code),
    INDEX idx_users_status (status),
    INDEX idx_users_locked_until (locked_until)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

--rollback DROP TABLE IF EXISTS users;
