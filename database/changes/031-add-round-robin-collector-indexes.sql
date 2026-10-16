--liquibase formatted sql

--changeset team5:EWSS22-031 dbms:mysql
--comment: Cover the active collector ring and most-recent collector lookup used by automatic round-robin assignment.
ALTER TABLE recycler_collector_scopes
    ADD INDEX idx_collector_scope_round_robin
        (recycler_org_id, zone, is_active, valid_from, valid_until, collector_org_id);

ALTER TABLE users
    ADD INDEX idx_users_active_collectors
        (organisation_id, role_code, status, user_id);

ALTER TABLE batch_assignments
    ADD INDEX idx_assignment_round_robin_recent
        (recycler_org_id, assigned_at DESC, assignment_sequence DESC, id);

--rollback ALTER TABLE batch_assignments DROP INDEX idx_assignment_round_robin_recent;
--rollback ALTER TABLE users DROP INDEX idx_users_active_collectors;
--rollback ALTER TABLE recycler_collector_scopes DROP INDEX idx_collector_scope_round_robin;
