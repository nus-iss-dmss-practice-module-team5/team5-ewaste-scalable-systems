--liquibase formatted sql

--changeset team5:EW101-C1-001 dbms:mysql
--comment: Create the organisation ownership boundary used by authentication and RBAC.
CREATE TABLE organisations (
    organisation_id      VARCHAR(32)  NOT NULL,
    organisation_name    VARCHAR(160) NOT NULL,
    organisation_type    VARCHAR(40)  NOT NULL,
    status               VARCHAR(24)  NOT NULL DEFAULT 'PENDING',
    created_at           TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at           TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                                          ON UPDATE CURRENT_TIMESTAMP(6),

    CONSTRAINT pk_organisations PRIMARY KEY (organisation_id),
    CONSTRAINT uq_organisations_name UNIQUE (organisation_name),
    CONSTRAINT ck_organisations_type CHECK (
        organisation_type IN (
            'PLATFORM',
            'DONOR',
            'COLLECTION_OPERATOR',
            'PROCESSING_FACILITY'
        )
    ),
    CONSTRAINT ck_organisations_status CHECK (
        status IN ('PENDING', 'ACTIVE', 'SUSPENDED', 'REJECTED', 'DISABLED')
    )
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

--rollback DROP TABLE IF EXISTS organisations;
