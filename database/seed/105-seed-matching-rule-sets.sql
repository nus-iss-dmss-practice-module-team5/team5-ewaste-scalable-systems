--liquibase formatted sql

--changeset team5:EWCSB129-105 dbms:mysql context:@seed labels:matching-configuration
--validCheckSum: 9:e6bf826ee57fbbec596986f0f65ffe25
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:1 SELECT COUNT(*) FROM users WHERE user_id = 'USR-001' AND organisation_id = 'PLATFORM' AND role_code = 'SYSTEM_ADMIN' AND status = 'ACTIVE';
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM matching_rule_sets WHERE version <> 'binary-v1' AND (id = 'a1290000-0000-4000-8000-000000000001' OR retired_at IS NULL OR retired_at > UTC_TIMESTAMP(6));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM matching_rule_sets WHERE version = 'binary-v1' AND ((NOT REGEXP_LIKE(id, '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$', 'c') AND BINARY id <> 'r2260000-0000-4000-8000-000000000001') OR effective_from > UTC_TIMESTAMP(6) OR retired_at IS NOT NULL OR rules_json <> CAST('{"categories":["ICT_EQUIPMENT","LARGE_APPLIANCE","BATTERIES","CONSUMER_ELECTRONICS"],"conditions":["FUNCTIONAL","REPAIRABLE","END_OF_LIFE"],"zones":["NORTH","SOUTH","EAST","WEST","CENTRAL"],"weight_scale":2,"rule_ids":["M1","M2","M3","M4","M5"],"predicates":{"M1":"ACTIVE_EXACT_CATEGORY","M2":"ACTIVE_CAPABILITY_AND_PROFILE_CONDITION_AND_DATA","M3":"OWNED_ACTIVE_POOL_FULL_WEIGHT","M4":"ACTIVE_EXACT_ZONE","M5":"EVALUATION_BEFORE_DEADLINE_AND_LEAD_ARRIVAL_AT_OR_BEFORE_DEADLINE"},"failure_precedence":["M1","M2","M3","M4","M5"],"ranking":false}' AS JSON));
--precondition-sql-check expectedResult:0 SELECT COUNT(*) FROM matching_rule_sets r WHERE BINARY r.id = 'r2260000-0000-4000-8000-000000000001' AND r.version = 'binary-v1' AND (EXISTS (SELECT 1 FROM matching_decisions d WHERE d.rule_set_id = r.id OR JSON_SEARCH(d.input_snapshot_json, 'one', r.id) IS NOT NULL) OR EXISTS (SELECT 1 FROM command_idempotency c WHERE JSON_SEARCH(c.response_json, 'one', r.id) IS NOT NULL) OR EXISTS (SELECT 1 FROM event_outbox e WHERE JSON_SEARCH(e.payload_json, 'one', r.id) IS NOT NULL) OR EXISTS (SELECT 1 FROM batch_audit_events a WHERE JSON_SEARCH(a.details_json, 'one', r.id) IS NOT NULL));
--comment: Initialize the binary-v1 matcher policy for dev/staging after the seeded administrator exists.
-- The policy is the exact $defs.policy.const from MatchingInput.v1.schema.json.
-- The validCheckSum above is the original 105 checksum: databases where that
-- seed succeeded keep their existing changelog entry and policy unchanged.
-- Retain an identical active policy, including its ID, creator and timestamps.
-- Repair only the reported non-UUID manual seed, and only without references;
-- never rewrite frozen inputs, decisions, audit history or published events.
-- Different content, invalid/retired policies and overlapping windows halt.
UPDATE matching_rule_sets
SET id = 'a1290000-0000-4000-8000-000000000001'
WHERE BINARY id = 'r2260000-0000-4000-8000-000000000001'
  AND version = 'binary-v1';

-- An empty installation activates the policy at migration time in UTC.
-- Liquibase runs this once; a valid pre-existing policy is a no-op.
INSERT INTO matching_rule_sets (
    id, version, rules_json, effective_from, retired_at, created_by, created_at
)
SELECT
    'a1290000-0000-4000-8000-000000000001',
    'binary-v1',
    '{
        "categories": ["ICT_EQUIPMENT", "LARGE_APPLIANCE", "BATTERIES", "CONSUMER_ELECTRONICS"],
        "conditions": ["FUNCTIONAL", "REPAIRABLE", "END_OF_LIFE"],
        "zones": ["NORTH", "SOUTH", "EAST", "WEST", "CENTRAL"],
        "weight_scale": 2,
        "rule_ids": ["M1", "M2", "M3", "M4", "M5"],
        "predicates": {
            "M1": "ACTIVE_EXACT_CATEGORY",
            "M2": "ACTIVE_CAPABILITY_AND_PROFILE_CONDITION_AND_DATA",
            "M3": "OWNED_ACTIVE_POOL_FULL_WEIGHT",
            "M4": "ACTIVE_EXACT_ZONE",
            "M5": "EVALUATION_BEFORE_DEADLINE_AND_LEAD_ARRIVAL_AT_OR_BEFORE_DEADLINE"
        },
        "failure_precedence": ["M1", "M2", "M3", "M4", "M5"],
        "ranking": false
    }',
    UTC_TIMESTAMP(6),
    NULL,
    'USR-001',
    UTC_TIMESTAMP(6)
WHERE NOT EXISTS (SELECT 1 FROM matching_rule_sets WHERE version = 'binary-v1');

-- No destructive rollback: matching decisions may reference this policy.
-- Production must provision its own approved administrator and activation.
