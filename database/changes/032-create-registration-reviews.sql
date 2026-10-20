--liquibase formatted sql

--changeset team5:EWSS28-032 dbms:mysql
--comment: Record the single Administrator decision on each self-registered collector or processor account.
CREATE TABLE registration_reviews (
    review_id         CHAR(36)     NOT NULL,
    user_id           VARCHAR(32)  NOT NULL,
    organisation_id   VARCHAR(32)  NOT NULL,
    decision          VARCHAR(16)  NOT NULL,
    reviewed_by       VARCHAR(32)  NOT NULL,
    reason            VARCHAR(255) NULL,
    correlation_id    VARCHAR(128) NOT NULL,
    reviewed_at       TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),

    CONSTRAINT pk_registration_reviews PRIMARY KEY (review_id),
    -- One decision per registration; a concurrent second decision fails here.
    CONSTRAINT uq_registration_reviews_user UNIQUE (user_id),
    CONSTRAINT fk_registration_reviews_user
        FOREIGN KEY (user_id) REFERENCES users (user_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_registration_reviews_organisation
        FOREIGN KEY (organisation_id) REFERENCES organisations (organisation_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_registration_reviews_reviewer
        FOREIGN KEY (reviewed_by) REFERENCES users (user_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT ck_registration_reviews_decision CHECK (
        decision IN ('APPROVED', 'REJECTED')
    ),

    INDEX idx_registration_reviews_reviewer (reviewed_by),
    INDEX idx_registration_reviews_reviewed_at (reviewed_at)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

--rollback DROP TABLE IF EXISTS registration_reviews;
