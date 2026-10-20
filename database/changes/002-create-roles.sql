--liquibase formatted sql

--changeset team5:EW101-C1-002 dbms:mysql
--comment: Create the role catalogue and record the organisation type allowed for each role.
CREATE TABLE roles (
    role_code                    VARCHAR(32)  NOT NULL,
    display_name                 VARCHAR(80)  NOT NULL,
    description                  VARCHAR(255) NOT NULL,
    allowed_organisation_type    VARCHAR(40)  NOT NULL,
    is_active                    BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at                   TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),

    CONSTRAINT pk_roles PRIMARY KEY (role_code),
    CONSTRAINT uq_roles_display_name UNIQUE (display_name),
    CONSTRAINT ck_roles_allowed_org_type CHECK (
        allowed_organisation_type IN (
            'PLATFORM',
            'DONOR',
            'COLLECTION_OPERATOR',
            'PROCESSING_FACILITY'
        )
    )
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

--rollback DROP TABLE IF EXISTS roles;
