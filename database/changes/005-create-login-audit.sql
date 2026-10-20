--liquibase formatted sql

--changeset team5:EW101-C1-005 dbms:mysql
--comment: Create append-only security audit records for authentication attempts.
CREATE TABLE login_audit (
    login_audit_id    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id           VARCHAR(32) NULL,
    attempted_email   VARCHAR(254) NOT NULL,
    result            VARCHAR(16) NOT NULL,
    reason_code       VARCHAR(40) NULL,
    correlation_id    CHAR(36) NOT NULL,
    source_ip         VARCHAR(45) NULL,
    occurred_at       TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),

    CONSTRAINT pk_login_audit
        PRIMARY KEY (login_audit_id),

    CONSTRAINT fk_login_audit_user
        FOREIGN KEY (user_id) REFERENCES users (user_id)
        ON UPDATE RESTRICT
        ON DELETE SET NULL,

    CONSTRAINT ck_login_audit_result
        CHECK (
            result IN ('SUCCESS', 'FAILURE')
        ),

    INDEX idx_login_audit_user (user_id),
    INDEX idx_login_audit_email (attempted_email),
    INDEX idx_login_audit_result (result),
    INDEX idx_login_audit_occurred_at (occurred_at),
    INDEX idx_login_audit_correlation (correlation_id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

--rollback DROP TABLE IF EXISTS login_audit;
