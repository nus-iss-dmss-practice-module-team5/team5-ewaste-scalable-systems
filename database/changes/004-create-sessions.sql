--liquibase formatted sql

--changeset team5:EW101-C1-004 dbms:mysql
--comment: Persist revocable server-side JWT session state; store only a SHA-256 hash of the current refresh token/JWT, never the raw token.
CREATE TABLE sessions (
    session_id          CHAR(36)     NOT NULL,
    user_id             VARCHAR(32)  NOT NULL,
    token_hash          CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    issued_at           TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at          TIMESTAMP(6) NOT NULL,
    last_seen_at        TIMESTAMP(6) NULL,
    revoked_at          TIMESTAMP(6) NULL,
    revocation_reason   VARCHAR(120) NULL,

    CONSTRAINT pk_sessions PRIMARY KEY (session_id),
    CONSTRAINT uq_sessions_token_hash UNIQUE (token_hash),

    CONSTRAINT fk_sessions_user
        FOREIGN KEY (user_id) REFERENCES users (user_id)
        ON UPDATE RESTRICT
        ON DELETE CASCADE,

    CONSTRAINT ck_sessions_expiry
        CHECK (expires_at > issued_at),

    CONSTRAINT ck_sessions_revocation
        CHECK (
            revoked_at IS NULL
            OR revoked_at >= issued_at
        ),

    INDEX idx_sessions_user (user_id),
    INDEX idx_sessions_expires_at (expires_at),
    INDEX idx_sessions_revoked_at (revoked_at)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

--rollback DROP TABLE IF EXISTS sessions;
