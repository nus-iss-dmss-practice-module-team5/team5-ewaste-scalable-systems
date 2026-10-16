#!/usr/bin/env bash
# Persistence suite for migrations 001-030 (Sprint 1, C1-C4 and processing).
# SQL checks, fixtures, concurrency probes and Docker configuration are embedded.
# Only production migrations, seed/repair changesets and database/Dockerfile are inputs.
# No src/ files, Go toolchain, or external test packages are required.
# This tests database guarantees; application command behavior is outside scope.
# Requires Bash, Git, Docker with Compose, and sha256sum or shasum.
# Run: ./scripts/test-persistence.sh
# PERSISTENCE_EVIDENCE_DIR optionally changes the parent of each fresh run folder.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
command -v docker >/dev/null || { echo 'Docker with Compose is required.' >&2; exit 1; }
docker compose version >/dev/null
docker info >/dev/null
if command -v sha256sum >/dev/null; then HASH=(sha256sum); else HASH=(shasum -a 256); fi
EVIDENCE_ROOT="${PERSISTENCE_EVIDENCE_DIR:-$ROOT_DIR/artifacts/persistence}"
mkdir -p "$EVIDENCE_ROOT"
EVIDENCE_ROOT="$(cd "$EVIDENCE_ROOT" && pwd)"
RUN_DIR="$(mktemp -d "$EVIDENCE_ROOT/$(date -u +%Y%m%dt%H%M%Sz)-XXXXXX")"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ewaste-persistence.XXXXXX")"
PROJECT="persistence-$(date -u +%Y%m%dt%H%M%Sz)-$$"
COMPOSE=(docker compose -p "$PROJECT" -f "$WORK_DIR/compose.yaml")
DOCKER_STARTED=0
finish() {
  local status=$?
  trap - EXIT
  if [[ "$DOCKER_STARTED" == 1 ]]; then
    if [[ "$status" != 0 ]]; then
      "${COMPOSE[@]}" logs --no-color mysql > "$RUN_DIR/mysql-failure.log" 2>&1 || true
    fi
    if ! "${COMPOSE[@]}" down --volumes > "$RUN_DIR/cleanup.log" 2>&1; then
      echo 'Test-container cleanup failed; see cleanup.log.' >&2
      status=1
    fi
  fi
  cd "$ROOT_DIR"
  rm -rf "$WORK_DIR"
  if [[ "$status" == 0 ]]; then printf 'PASS\n' > "$RUN_DIR/result.txt";
  else printf 'FAIL\n' > "$RUN_DIR/result.txt"; fi
  printf 'Exit status: %s\nCombined evidence: %s\n' "$status" "$RUN_DIR"
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Copy production inputs and the two processing SQL suites. Legacy tests remain embedded.
cd "$ROOT_DIR"
git rev-parse HEAD > "$RUN_DIR/base-commit.txt"
git branch --show-current > "$RUN_DIR/branch.txt"
"${HASH[@]}" scripts/test-persistence.sh database/Dockerfile database/changelog-master.yaml \
  database/changes/*.sql database/seed/10[1235678]-*.sql \
  database/tests/test-processing-fixtures.sql database/tests/verify-processing-migrations.sql > "$RUN_DIR/source-inputs.sha256"
mkdir -p "$WORK_DIR/database/changes" "$WORK_DIR/database/seed"
cp database/Dockerfile database/changelog-master.yaml "$WORK_DIR/database/"
cp database/changes/*.sql "$WORK_DIR/database/changes/"
mkdir -p "$WORK_DIR/database/tests"
cp database/tests/test-processing-fixtures.sql database/tests/verify-processing-migrations.sql "$WORK_DIR/database/tests/"
cp database/seed/10[1235678]-*.sql "$WORK_DIR/database/seed/"

mysql_query() {
  local db="$1"; shift
  "${COMPOSE[@]}" exec -T -e MYSQL_PWD=persistence-test-only mysql mysql \
    --default-character-set=utf8mb4 --batch --raw -upersistence_test "$db" "$@"
}
lb() {
  local db="$1" label="$2"; shift 2
  "${COMPOSE[@]}" run --rm -T \
    -e "LIQUIBASE_COMMAND_CHANGELOG_FILE=$CHANGELOG" \
    -e "LIQUIBASE_COMMAND_URL=jdbc:mysql://mysql:3306/$db?useSSL=false&allowPublicKeyRetrieval=true&serverTimezone=UTC" \
    liquibase "$@" > "$EVIDENCE_DIR/$db-$label.log" 2>&1 || {
      cat "$EVIDENCE_DIR/$db-$label.log" >&2; return 1;
    }
}
check_scalar() {
  local db="$1" name="$2" query="$3" expected="$4" actual
  actual="$(mysql_query "$db" --skip-column-names -e "$query")"
  printf '%s\t%s\t%s\n' "$name" "$actual" "$expected" >> "$EVIDENCE_DIR/migration-checks.tsv"
  [[ "$actual" == "$expected" ]] || { printf 'FAIL %s: %s != %s\n' "$name" "$actual" "$expected" >&2; return 1; }
}
reject_matching_seed() {
  local db="$1" name="$2"
  if lb "$db" "$name" --changelog-file=seed/105-seed-matching-rule-sets.sql update --context-filter=seed; then
    echo "FAIL: $name unexpectedly applied the matching policy." >&2; return 1
  fi
  grep -q 'SQL Precondition failed' "$EVIDENCE_DIR/$db-$name.log"
  check_scalar "$db" "$name" "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-105'" 0
}
dump_rows() {
  local db="$1" dest="$2"; shift 2
  "${COMPOSE[@]}" exec -T -e MYSQL_PWD=persistence-test-only mysql mysqldump -upersistence_test \
    --default-character-set=utf8mb4 --no-tablespaces --no-create-info --skip-extended-insert \
    --compact --skip-add-locks --skip-disable-keys --order-by-primary --skip-comments --skip-triggers \
    "$db" "$@" > "$dest"
}
# These are direct SQL fixtures for database locking, uniqueness and rollback.
# They do not implement or verify application authorization or command semantics.
run_sql_race() {
  local db="$1" name="$2" lock_sql="$3" winner_body="$4" loser_body="$5" duplicate_key="$6"
  local ready_lock="persistence-$db-$name" winner_pid loser_pid observed=0 ready=0 attempt
  local winner_status=0 loser_status=0
  mkdir -p "$WORK_DIR/races"
  {
    printf "SET time_zone='+00:00';\nSTART TRANSACTION;\n%s;\n" "$lock_sql"
    # Expose readiness only after holding the row lock. The bounded pause gives
    # the other client time to reach a real InnoDB lock wait, which we verify.
    printf "SELECT GET_LOCK('%s',0);\nSELECT SLEEP(5);\n" "$ready_lock"
    cat "$winner_body"
    printf '\nCOMMIT;\n'
  } > "$WORK_DIR/races/$name-winner.sql"
  {
    printf "SET time_zone='+00:00';\nSTART TRANSACTION;\n%s;\n" "$lock_sql"
    cat "$loser_body"
    printf '\nCOMMIT;\n'
  } > "$WORK_DIR/races/$name-loser.sql"
  ( trap - EXIT INT TERM; mysql_query "$db" < "$WORK_DIR/races/$name-winner.sql" ) > "$EVIDENCE_DIR/$name-winner.txt" 2>&1 &
  winner_pid=$!
  for ((attempt=0; attempt<100; attempt++)); do
    ready="$(mysql_query "$db" --skip-column-names -e "SELECT IS_USED_LOCK('$ready_lock') IS NOT NULL")"
    [[ "$ready" == 1 ]] && break
    sleep 0.05
  done
  if [[ "$ready" != 1 ]]; then
    wait "$winner_pid" || true
    cat "$EVIDENCE_DIR/$name-winner.txt" >&2
    echo "FAIL: $name did not acquire its row lock." >&2; return 1
  fi
  # The loser's error is kept apart from its result rows. Docker relays the two
  # streams separately, so in one file a row can land inside the error line.
  ( trap - EXIT INT TERM; mysql_query "$db" < "$WORK_DIR/races/$name-loser.sql" ) \
    > "$EVIDENCE_DIR/$name-loser.txt" 2> "$EVIDENCE_DIR/$name-loser-error.txt" &
  loser_pid=$!
  for ((attempt=0; attempt<100; attempt++)); do
    observed="$(mysql_query "$db" --skip-column-names -e 'SELECT COUNT(*)>0 FROM performance_schema.data_lock_waits')"
    [[ "$observed" == 1 ]] && break
    sleep 0.05
  done
  wait "$winner_pid" || winner_status=$?
  wait "$loser_pid" || loser_status=$?
  if [[ "$observed" != 1 || "$winner_status" != 0 || "$loser_status" == 0 ]] ||
      ! grep -Eq "ERROR 1062 .*($duplicate_key)" "$EVIDENCE_DIR/$name-loser-error.txt"; then
    cat "$EVIDENCE_DIR/$name-winner.txt" "$EVIDENCE_DIR/$name-loser.txt" "$EVIDENCE_DIR/$name-loser-error.txt" >&2
    echo "FAIL: $name did not produce one committed winner and one duplicate-key rollback after a real lock wait." >&2
    return 1
  fi
  printf '%s\tPASS\t%s\t%s\t%s\t%s\n' "$name" "$winner_status" "$loser_status" "$observed" "$duplicate_key" >> "$EVIDENCE_DIR/concurrency.tsv"
}

run_claim_sql_concurrency() {
  local side claim reservation
  printf 'case\tresult\twinner_exit\tloser_exit\tlock_wait_observed\trejected_key\n' > "$EVIDENCE_DIR/concurrency.tsv"
  mkdir -p "$WORK_DIR/races"
  for side in 1 2; do
    claim="f3900000-0000-4000-8000-00000000000$side"
    reservation="a3900000-0000-4000-8000-00000000000$side"
    cat > "$WORK_DIR/races/claim-$side.sql" <<SQL_CLAIM_RACE
INSERT INTO batch_claims (id,batch_id,claim_epoch,recycler_org_id,claimed_by,idempotency_key,claimed_at,created_at)
VALUES ('$claim','b3000000-0000-4000-8000-000000000002',1,'PROC-001','USR-007',
    'SQL-Concurrent-Claim-000$side','2026-09-19 09:00:00','2026-09-19 09:00:00');
INSERT INTO capacity_reservations (id,batch_id,claim_id,capacity_pool_id,reserved_kg,status,reserved_at,version)
VALUES ('$reservation','b3000000-0000-4000-8000-000000000002','$claim',
    'd3000000-0000-4000-8000-000000000001',100,'RESERVED','2026-09-19 09:00:00',1);
UPDATE recycler_capacity_pools SET reserved_kg=reserved_kg+100,version=version+1,updated_at='2026-09-19 09:00:00'
WHERE id='d3000000-0000-4000-8000-000000000001';
UPDATE ewaste_batches SET status='APPROVED',current_claim_id='$claim',version=version+1,updated_at='2026-09-19 09:00:00'
WHERE id='b3000000-0000-4000-8000-000000000002';
SQL_CLAIM_RACE
  done
  run_sql_race c3_clean claim_epoch \
    "SELECT id FROM ewaste_batches WHERE id='b3000000-0000-4000-8000-000000000002' FOR UPDATE" \
    "$WORK_DIR/races/claim-1.sql" "$WORK_DIR/races/claim-2.sql" uk_batch_claim_epoch
  check_scalar c3_clean race_claim_count "SELECT COUNT(*) FROM batch_claims WHERE batch_id='b3000000-0000-4000-8000-000000000002'" 1
  check_scalar c3_clean race_reservation_count "SELECT COUNT(*) FROM capacity_reservations WHERE batch_id='b3000000-0000-4000-8000-000000000002'" 1
  check_scalar c3_clean race_pool_one_increment "SELECT CONCAT(reserved_kg,':',version) FROM recycler_capacity_pools WHERE id='d3000000-0000-4000-8000-000000000001'" '200.00:3'
  check_scalar c3_clean race_claim_pointer "SELECT CONCAT(status,':',version,':',current_claim_id) FROM ewaste_batches WHERE id='b3000000-0000-4000-8000-000000000002'" 'APPROVED:4:f3900000-0000-4000-8000-000000000001'
}

run_assignment_sql_concurrency() {
  local side assignment actor org scope command outcome collected name count proof reason
  printf 'case\tresult\twinner_exit\tloser_exit\tlock_wait_observed\trejected_key\n' > "$EVIDENCE_DIR/concurrency.tsv"
  mysql_query c4_clean <<'SQL_ASSIGNMENT_RACE_SETUP'
SET time_zone='+00:00';
START TRANSACTION;
INSERT INTO ewaste_batches (id,organization_id,created_by,status,category,quantity,estimated_weight_kg,
    condition_rating,is_data_bearing,zone,collection_deadline,claim_epoch,version,submitted_at,created_at,updated_at)
VALUES ('b4900000-0000-4000-8000-000000000001','DON-001','USR-003','MATCHED','ICT_EQUIPMENT',5,100,
    'REPAIRABLE',1,'CENTRAL','2026-09-25 10:00:00',1,3,'2026-09-17 10:00:00','2026-09-17 09:00:00','2026-09-18 09:00:00');
INSERT INTO batch_claims (id,batch_id,claim_epoch,recycler_org_id,claimed_by,idempotency_key,claimed_at,created_at)
VALUES ('f4900000-0000-4000-8000-000000000001','b4900000-0000-4000-8000-000000000001',1,'PROC-001','USR-007',
    'SQL-Assignment-Claim-0001','2026-09-18 10:00:00','2026-09-18 10:00:00');
INSERT INTO capacity_reservations (id,batch_id,claim_id,capacity_pool_id,reserved_kg,status,reserved_at,version)
VALUES ('a4900001-0000-4000-8000-000000000001','b4900000-0000-4000-8000-000000000001',
    'f4900000-0000-4000-8000-000000000001','d3000000-0000-4000-8000-000000000001',100,'RESERVED','2026-09-18 10:00:00',1);
UPDATE recycler_capacity_pools SET reserved_kg=reserved_kg+100,version=version+1 WHERE id='d3000000-0000-4000-8000-000000000001';
UPDATE ewaste_batches SET status='APPROVED',version=4,current_claim_id='f4900000-0000-4000-8000-000000000001'
WHERE id='b4900000-0000-4000-8000-000000000001';
COMMIT;
SQL_ASSIGNMENT_RACE_SETUP
  dump_rows c4_clean "$EVIDENCE_DIR/held-before-races.sql" batch_claims capacity_reservations recycler_capacity_pools
  for side in 1 2; do
    assignment="a4900000-0000-4000-8000-00000000000$side"
    if [[ "$side" == 1 ]]; then actor=USR-005; org=COL-001; else actor=USR-006; org=COL-002; fi
    scope="54000000-0000-4000-8000-00000000000$side"
    cat > "$WORK_DIR/races/assignment-$side.sql" <<SQL_ASSIGNMENT_RACE
INSERT INTO batch_assignments (id,batch_id,claim_id,recycler_org_id,collector_org_id,collector_user_id,collector_scope_id,
    assignment_sequence,claim_epoch,assignment_status,assigned_at,responded_at,version,created_at,updated_at)
VALUES ('$assignment','b4900000-0000-4000-8000-000000000001','f4900000-0000-4000-8000-000000000001',
    'PROC-001','$org','$actor','$scope',1,1,'ACCEPTED','2026-09-19 09:00:00','2026-09-19 09:00:00',1,
    '2026-09-19 09:00:00','2026-09-19 09:00:00');
UPDATE ewaste_batches SET status='ASSIGNED',current_assignment_id='$assignment',version=version+1,updated_at='2026-09-19 09:00:00'
WHERE id='b4900000-0000-4000-8000-000000000001';
SQL_ASSIGNMENT_RACE
  done
  run_sql_race c4_clean assignment_slot \
    "SELECT id FROM ewaste_batches WHERE id='b4900000-0000-4000-8000-000000000001' FOR UPDATE" \
    "$WORK_DIR/races/assignment-1.sql" "$WORK_DIR/races/assignment-2.sql" 'uq_assignment_sequence|uq_assignment_open'
  check_scalar c4_clean race_assignment_count "SELECT COUNT(*) FROM batch_assignments WHERE batch_id='b4900000-0000-4000-8000-000000000001'" 1
  check_scalar c4_clean race_assignment_pointer "SELECT CONCAT(status,':',version,':',current_assignment_id) FROM ewaste_batches WHERE id='b4900000-0000-4000-8000-000000000001'" 'ASSIGNED:5:a4900000-0000-4000-8000-000000000001'
  for side in 1 2; do
    command="c4900000-0000-4000-8000-00000000000$side"
    if [[ "$side" == 1 ]]; then
      outcome=COLLECTED; collected="'2026-09-19 10:00:00'"; name="'Synthetic Representative'"; count=5; proof="REPEAT('a',64)"; reason=NULL
    else
      outcome=FAILED_COLLECTION; collected=NULL; name=NULL; count=NULL; proof=NULL; reason="'DONOR_UNAVAILABLE'"
    fi
    cat > "$WORK_DIR/races/handoff-$side.sql" <<SQL_HANDOFF_RACE
INSERT INTO command_idempotency (id,actor_user_id,actor_scope,command_name,idempotency_key,request_hash,batch_id,assignment_id,
    state,created_at,retain_until)
VALUES ('$command','USR-005','user:USR-005','SQLSchemaHandoff','SQL-Handoff-Race-000$side',SHA2('SQL race $side',256),
    'b4900000-0000-4000-8000-000000000001','a4900000-0000-4000-8000-000000000001','IN_PROGRESS',
    '2026-09-19 10:00:00','2027-09-19 10:00:00');
INSERT INTO batch_handoffs (id,batch_id,assignment_id,collector_user_id,collector_org_id,pickup_status,donor_representative_name,
    actual_item_count,verification_hash,failure_reason,pickup_occurred_at,recorded_at,collected_at,command_id,correlation_id,created_at)
VALUES ('64900000-0000-4000-8000-00000000000$side','b4900000-0000-4000-8000-000000000001',
    'a4900000-0000-4000-8000-000000000001','USR-005','COL-001','$outcome',$name,$count,$proof,$reason,
    '2026-09-19 10:00:00','2026-09-19 10:00:00',$collected,'$command','sql-handoff-race','2026-09-19 10:00:00');
UPDATE batch_assignments SET assignment_status=IF('$outcome'='COLLECTED','COMPLETED','FAILED'),closed_at='2026-09-19 10:00:00',
    closure_reason='$outcome',version=version+1,updated_at='2026-09-19 10:00:00' WHERE id='a4900000-0000-4000-8000-000000000001';
UPDATE ewaste_batches SET status='$outcome',version=version+1,updated_at='2026-09-19 10:00:00'
WHERE id='b4900000-0000-4000-8000-000000000001';
UPDATE command_idempotency SET state='COMPLETED',response_status=200,response_json=JSON_OBJECT('fixture','SQL constraints'),
    completed_at='2026-09-19 10:00:00' WHERE id='$command';
SQL_HANDOFF_RACE
  done
  run_sql_race c4_clean handoff_outcome \
    "SELECT id FROM ewaste_batches WHERE id='b4900000-0000-4000-8000-000000000001' FOR UPDATE" \
    "$WORK_DIR/races/handoff-1.sql" "$WORK_DIR/races/handoff-2.sql" uq_handoff_assignment
  check_scalar c4_clean race_handoff_count "SELECT COUNT(*) FROM batch_handoffs WHERE assignment_id='a4900000-0000-4000-8000-000000000001'" 1
  check_scalar c4_clean race_handoff_result "SELECT pickup_status FROM batch_handoffs WHERE assignment_id='a4900000-0000-4000-8000-000000000001'" COLLECTED
  check_scalar c4_clean race_losing_command_rolled_back "SELECT COUNT(*) FROM command_idempotency WHERE id='c4900000-0000-4000-8000-000000000002'" 0
  check_scalar c4_clean race_handoff_batch_version "SELECT CONCAT(status,':',version) FROM ewaste_batches WHERE id='b4900000-0000-4000-8000-000000000001'" 'COLLECTED:6'
  check_scalar c4_clean race_handoff_assignment_version "SELECT CONCAT(assignment_status,':',version) FROM batch_assignments WHERE id='a4900000-0000-4000-8000-000000000001'" 'COMPLETED:2'
  dump_rows c4_clean "$EVIDENCE_DIR/held-after-races.sql" batch_claims capacity_reservations recycler_capacity_pools
  diff -u "$EVIDENCE_DIR/held-before-races.sql" "$EVIDENCE_DIR/held-after-races.sql" > "$EVIDENCE_DIR/held-races-diff.txt"
}

# Embedded test SQL and configuration are materialized only in WORK_DIR.
write_embedded_tests() {
  # Embedded compose.yaml
  cat > "$WORK_DIR/compose.yaml" <<'PERSISTENCE_EMBED_000'
services:
  mysql:
    image: mysql:8.4
    command: ["--max-connections=180"]
    environment:
      MYSQL_ROOT_PASSWORD: persistence-root-test-only
      MYSQL_USER: persistence_test
      MYSQL_PASSWORD: persistence-test-only
      MYSQL_DATABASE: c1_clean
      TZ: UTC
    volumes:
      - data:/var/lib/mysql
    healthcheck:
      # Checked over TCP. On first start the image runs a temporary server
      # that listens on the socket only and is then shut down; a socket check
      # can pass against it just before it goes away.
      test: [CMD-SHELL, 'MYSQL_PWD="$$MYSQL_ROOT_PASSWORD" mysql -h127.0.0.1 -uroot -e "SELECT 1" >/dev/null 2>&1']
      interval: 2s
      timeout: 5s
      retries: 60
      start_period: 20s
  liquibase:
    build:
      context: ./database
      dockerfile: Dockerfile
    working_dir: /liquibase/changelog
    volumes:
      - ./database:/liquibase/changelog:ro
    environment:
      LIQUIBASE_COMMAND_USERNAME: persistence_test
      LIQUIBASE_COMMAND_PASSWORD: persistence-test-only
      LIQUIBASE_SEARCH_PATH: /liquibase/changelog
volumes:
  data:
PERSISTENCE_EMBED_000

  # Embedded approved-migrations.sha256
  cat > "$WORK_DIR/approved-migrations.sha256" <<'PERSISTENCE_EMBED_001'
facea189139422d24a5350457f9382ea11e5a53b586f881d2e8adf06ef5fdf2d  database/changes/006-create-ewaste-batches.sql
750092fd220eae655e5bd71ca13e9114ab1b236832ad78e605d8485be42e2f6d  database/changes/007-add-ewaste-batch-constraints.sql
d9c717bd840d97817492c8512f327fdb36f56daa4de7e3180a52897719d317a7  database/changes/008-create-command-idempotency.sql
dde62efcb3d2fc97794a20fa26c9bb49235e577033f8d7945ebf144d9e9427fe  database/changes/009-create-batch-audit-events.sql
c5f7f67570d734cf22b63beffbb160c20e1d443bd7e6ab280a5f0107cb72aba5  database/changes/010-create-event-outbox.sql
cccc483ca42faacb11f9cfdf44146e81ce9790eb65d8479454647df7e73a3156  database/changes/011-create-matching-rule-sets.sql
285e36ceaaf0436a74e6179fc90545851c5f11e8920d37fda09f22f22deab6a9  database/changes/012-create-recycler-matching-profiles.sql
8c0eb85996401674aa53ebb933c2d83119236d9de234f600b6d3d88d08d1a88d  database/changes/013-create-recycler-capacity-pools.sql
b0631e3749707f889d7640a4d1e0965733fa62732fc304b92d4cb86f7f1d109e  database/changes/014-create-recycler-category-capabilities.sql
31c30af89685e532baabeca1154c0b4bfcfe639ccc8238045294063e93c5de33  database/changes/015-create-recycler-service-zones.sql
659685b17d5ff195b2c00021e4d23926789969c1442dd603de83707549e8b479  database/changes/016-create-matching-decisions.sql
63d835ada9140dfdeef36ecb11b0c15934b506c815bdfcb05ae00928cabf3004  database/changes/017-create-matched-results.sql
83856646edbfc5d6212d0b4346ae5b1c2216619c84d8ed5236c9ebd90e757d09  database/changes/018-create-batch-claims.sql
218011f9a6670f5a24a70014c45fc66e9f9074d9134c91b1f5a3147c41cdd44f  database/changes/019-create-capacity-reservations.sql
1651e5e61ebee8450f6cb6f5a9a940a46922b8f902cd350247a2e140c74a081a  database/changes/020-link-current-claim-and-audit.sql
7b61a1b5e62574d9dd98801efbc2802800d82d33a8349c192d22457f074d926a  database/changes/021-create-recycler-collector-scopes.sql
14ebedd497ecb4a6c6b49285e874dbbd96c46014fc02094fa33c250f89c9fff4  database/changes/022-create-batch-assignments.sql
b47e8efdfd0adb3dd4ead07d93ca500e9c67bd963bedbe27122b1090cf883920  database/changes/023-create-batch-handoffs.sql
ae9ff88f61bf6ecb78866b5bcf32ff0139baa37f06b856b8c6cdb7afc8780991  database/changes/024-create-assignment-actions.sql
d1850f775137ee9c0bc6ff82fcd8bc7f206b8ed98e4ef8a3dfa77618111c8ca2  database/changes/025-link-assignment-pointers-and-history.sql
PERSISTENCE_EMBED_001

  mkdir -p "$WORK_DIR/database/tests"
  # Embedded database/tests/verify-ew101-c1.sql
  cat > "$WORK_DIR/database/tests/verify-ew101-c1.sql" <<'PERSISTENCE_EMBED_002'
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
PERSISTENCE_EMBED_002

  mkdir -p "$WORK_DIR/database/tests/c1"
  # Embedded database/tests/c1/changelog-sprint1.yaml
  cat > "$WORK_DIR/database/tests/c1/changelog-sprint1.yaml" <<'PERSISTENCE_EMBED_003'
# Exact pre-C1 include paths preserve existing DATABASECHANGELOG identities.
databaseChangeLog:
  - include:
      file: changes/001-create-organisations.sql
  - include:
      file: changes/002-create-roles.sql
  - include:
      file: changes/003-create-users.sql
  - include:
      file: changes/004-create-sessions.sql
  - include:
      file: changes/005-create-login-audit.sql
  - include:
      file: seed/101-seed-organisations.sql
  - include:
      file: seed/102-seed-roles.sql
  - include:
      file: seed/103-seed-users.sql
PERSISTENCE_EMBED_003

  mkdir -p "$WORK_DIR/database/tests/c1"
  # Embedded database/tests/c1/changelog-c1.yaml
  cat > "$WORK_DIR/database/tests/c1/changelog-c1.yaml" <<'PERSISTENCE_EMBED_004'
# Frozen C1 test boundary; includes preserve the original Liquibase file identities.
databaseChangeLog:
  - include:
      file: changes/001-create-organisations.sql
  - include:
      file: changes/002-create-roles.sql
  - include:
      file: changes/003-create-users.sql
  - include:
      file: changes/004-create-sessions.sql
  - include:
      file: changes/005-create-login-audit.sql
  - include:
      file: changes/006-create-ewaste-batches.sql
  - include:
      file: changes/007-add-ewaste-batch-constraints.sql
  - include:
      file: changes/008-create-command-idempotency.sql
  - include:
      file: changes/009-create-batch-audit-events.sql
  - include:
      file: changes/010-create-event-outbox.sql
  - include:
      file: seed/101-seed-organisations.sql
  - include:
      file: seed/102-seed-roles.sql
  - include:
      file: seed/103-seed-users.sql
  - include:
      file: seed/104-seed-c1-batches.sql
PERSISTENCE_EMBED_004

  mkdir -p "$WORK_DIR/database/tests/c1"
  # Embedded database/tests/c1/verify.sql
  cat > "$WORK_DIR/database/tests/c1/verify.sql" <<'PERSISTENCE_EMBED_005'
-- EWCSB-126 runtime checks. Run only in the runner's disposable MySQL database.
-- Fixture mutations are rolled back. The transaction test commits one extra batch.
-- Expected errors include the exact MySQL error number and named constraint.
SET NAMES utf8mb4;
SET time_zone = '+00:00';
SET SESSION group_concat_max_len = 16384;
CREATE TEMPORARY TABLE c1_results (
    sequence_id INT AUTO_INCREMENT PRIMARY KEY,
    test_name VARCHAR(128) NOT NULL UNIQUE,
    result VARCHAR(4) NOT NULL,
    actual VARCHAR(4096),
    expected VARCHAR(4096)
) ENGINE=MEMORY;
DELIMITER $$
CREATE PROCEDURE c1_assert(IN p_name VARCHAR(128), IN p_actual TEXT, IN p_expected TEXT)
BEGIN
    INSERT INTO c1_results(test_name, result, actual, expected)
    VALUES(p_name, IF(BINARY p_actual <=> BINARY p_expected, 'PASS', 'FAIL'), p_actual, p_expected);
END$$
CREATE PROCEDURE c1_statement(
    IN p_name VARCHAR(128), IN p_sql LONGTEXT,
    IN p_errno INT, IN p_constraint VARCHAR(128))
BEGIN
    DECLARE observed INT DEFAULT 0;
    DECLARE message_text_value TEXT DEFAULT '';
    DECLARE affected INT DEFAULT 0;
    DECLARE prepared_ok BOOLEAN DEFAULT FALSE;
    START TRANSACTION;
    BEGIN
        DECLARE EXIT HANDLER FOR SQLEXCEPTION
            GET DIAGNOSTICS CONDITION 1 observed = MYSQL_ERRNO, message_text_value = MESSAGE_TEXT;
        SET @c1_statement = p_sql;
        PREPARE c1_prepared FROM @c1_statement;
        SET prepared_ok = TRUE;
        EXECUTE c1_prepared;
        SET affected = ROW_COUNT();
    END;
    IF prepared_ok THEN DEALLOCATE PREPARE c1_prepared; END IF;
    ROLLBACK;
    INSERT INTO c1_results(test_name, result, actual, expected)
    VALUES(p_name,
        IF(observed = p_errno
           AND (p_errno <> 0 OR affected = 1)
           AND (p_constraint = '' OR LOCATE(p_constraint, message_text_value) > 0), 'PASS', 'FAIL'),
        IF(observed = 0, CONCAT('accepted rows=', affected), CONCAT(observed, ': ', message_text_value)),
        IF(p_errno = 0, 'accepted rows=1', CONCAT(p_errno, ': ', p_constraint)));
END$$
CREATE PROCEDURE c1_clone(
    IN p_name VARCHAR(128), IN p_table VARCHAR(64), IN p_source VARCHAR(36),
    IN p_id VARCHAR(36), IN p_patch JSON, IN p_errno INT, IN p_constraint VARCHAR(128))
BEGIN
    DECLARE columns_sql TEXT;
    DECLARE values_sql TEXT;
    DECLARE pk_name VARCHAR(16);
    IF p_table NOT IN ('ewaste_batches','command_idempotency','batch_audit_events','event_outbox') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Invalid test table';
    END IF;
    SET pk_name = IF(p_table = 'event_outbox', 'event_id', 'id');
    SELECT GROUP_CONCAT(CONCAT('`', COLUMN_NAME, '`') ORDER BY ORDINAL_POSITION),
           GROUP_CONCAT(CASE
               WHEN JSON_CONTAINS_PATH(p_patch, 'one', CONCAT('$.', COLUMN_NAME))
                   THEN JSON_UNQUOTE(JSON_EXTRACT(p_patch, CONCAT('$.', COLUMN_NAME)))
               WHEN COLUMN_NAME = pk_name THEN QUOTE(p_id)
               ELSE CONCAT('b.`', COLUMN_NAME, '`') END ORDER BY ORDINAL_POSITION)
    INTO columns_sql, values_sql
    FROM information_schema.columns WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = p_table;
    CALL c1_statement(p_name,
        CONCAT('INSERT INTO ', p_table, ' (', columns_sql, ') SELECT ', values_sql,
               ' FROM ', p_table, ' b WHERE b.', pk_name, ' = ', QUOTE(p_source)),
        p_errno, p_constraint);
END$$
CREATE PROCEDURE c1_finish()
BEGIN
    IF EXISTS(SELECT 1 FROM c1_results WHERE result <> 'PASS') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'EWCSB-126 persistence check failed';
    END IF;
END$$
DELIMITER ;

CALL c1_assert('runtime.mysql_8_4', CAST((VERSION() LIKE '8.4.%') AS CHAR), '1');

CALL c1_assert('runtime.strict_mode', CAST((FIND_IN_SET('STRICT_TRANS_TABLES', @@sql_mode) > 0 OR FIND_IN_SET('STRICT_ALL_TABLES', @@sql_mode) > 0) AS CHAR), '1');

CALL c1_assert('runtime.utc', CAST((@@session.time_zone) AS CHAR), '+00:00');

CALL c1_assert('schema.ewaste_batches_columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches') AS CHAR), '19');

CALL c1_assert('schema.ewaste_batches_engine', CAST((SELECT CONCAT(ENGINE, '/', TABLE_COLLATION) FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c1_assert('schema.command_idempotency_columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='command_idempotency') AS CHAR), '15');

CALL c1_assert('schema.command_idempotency_engine', CAST((SELECT CONCAT(ENGINE, '/', TABLE_COLLATION) FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='command_idempotency') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c1_assert('schema.batch_audit_events_columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='batch_audit_events') AS CHAR), '16');

CALL c1_assert('schema.batch_audit_events_engine', CAST((SELECT CONCAT(ENGINE, '/', TABLE_COLLATION) FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='batch_audit_events') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c1_assert('schema.event_outbox_columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='event_outbox') AS CHAR), '18');

CALL c1_assert('schema.event_outbox_engine', CAST((SELECT CONCAT(ENGINE, '/', TABLE_COLLATION) FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='event_outbox') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c1_assert('schema.batch.id', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='id') AS CHAR), 'varchar(36)/NO');

CALL c1_assert('schema.batch.organization_id', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='organization_id') AS CHAR), 'varchar(32)/NO');

CALL c1_assert('schema.batch.created_by', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='created_by') AS CHAR), 'varchar(32)/NO');

CALL c1_assert('schema.batch.status', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='status') AS CHAR), 'varchar(32)/NO');

CALL c1_assert('schema.batch.category', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='category') AS CHAR), 'varchar(32)/YES');

CALL c1_assert('schema.batch.quantity', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='quantity') AS CHAR), 'int unsigned/YES');

CALL c1_assert('schema.batch.estimated_weight_kg', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='estimated_weight_kg') AS CHAR), 'decimal(8,2)/YES');

CALL c1_assert('schema.batch.condition_rating', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='condition_rating') AS CHAR), 'varchar(32)/YES');

CALL c1_assert('schema.batch.is_data_bearing', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='is_data_bearing') AS CHAR), 'tinyint(1)/NO');

CALL c1_assert('schema.batch.zone', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='zone') AS CHAR), 'varchar(16)/YES');

CALL c1_assert('schema.batch.collection_deadline', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='collection_deadline') AS CHAR), 'datetime(6)/YES');

CALL c1_assert('schema.batch.notes', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='notes') AS CHAR), 'varchar(500)/YES');

CALL c1_assert('schema.batch.claim_epoch', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='claim_epoch') AS CHAR), 'bigint unsigned/NO');

CALL c1_assert('schema.batch.current_claim_id', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='current_claim_id') AS CHAR), 'varchar(36)/YES');

CALL c1_assert('schema.batch.current_assignment_id', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='current_assignment_id') AS CHAR), 'varchar(36)/YES');

CALL c1_assert('schema.batch.version', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='version') AS CHAR), 'int unsigned/NO');

CALL c1_assert('schema.batch.submitted_at', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='submitted_at') AS CHAR), 'datetime(6)/YES');

CALL c1_assert('schema.batch.created_at', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='created_at') AS CHAR), 'datetime(6)/NO');

CALL c1_assert('schema.batch.updated_at', CAST((SELECT CONCAT(COLUMN_TYPE, '/', IS_NULLABLE) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME='updated_at') AS CHAR), 'datetime(6)/NO');

CALL c1_assert('schema.case_sensitive_codes', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND COLUMN_NAME IN ('status','category','condition_rating','zone') AND COLLATION_NAME='utf8mb4_0900_as_cs') AS CHAR), '4');

CALL c1_assert('schema.identity_fk_collations', CAST((SELECT COUNT(*) FROM information_schema.columns child JOIN information_schema.columns parent ON parent.TABLE_SCHEMA=child.TABLE_SCHEMA AND ((child.COLUMN_NAME='organization_id' AND parent.TABLE_NAME='organisations' AND parent.COLUMN_NAME='organisation_id') OR (child.COLUMN_NAME='created_by' AND parent.TABLE_NAME='users' AND parent.COLUMN_NAME='user_id')) WHERE child.TABLE_SCHEMA=DATABASE() AND child.TABLE_NAME='ewaste_batches' AND child.COLUMN_TYPE=parent.COLUMN_TYPE AND child.COLLATION_NAME=parent.COLLATION_NAME) AS CHAR), '2');

CALL c1_assert('schema.replay_key_case_sensitive', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='command_idempotency' AND COLUMN_NAME IN ('actor_scope','command_name','idempotency_key') AND COLLATION_NAME='utf8mb4_0900_as_cs') AS CHAR), '3');

CALL c1_assert('schema.ewaste_batches_enforced_checks', CAST((SELECT GROUP_CONCAT(CONSTRAINT_NAME ORDER BY CONSTRAINT_NAME) FROM information_schema.table_constraints WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND CONSTRAINT_TYPE='CHECK' AND ENFORCED='YES') AS CHAR), 'ck_batches_c1_assignment_null,ck_batches_c1_claim_null,ck_batches_category,ck_batches_condition,ck_batches_data_bearing,ck_batches_epoch,ck_batches_quantity,ck_batches_status,ck_batches_submit_completeness,ck_batches_submitted_time,ck_batches_updated_time,ck_batches_version,ck_batches_weight,ck_batches_zone');

CALL c1_assert('schema.command_idempotency_enforced_checks', CAST((SELECT GROUP_CONCAT(CONSTRAINT_NAME ORDER BY CONSTRAINT_NAME) FROM information_schema.table_constraints WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME='command_idempotency' AND CONSTRAINT_TYPE='CHECK' AND ENFORCED='YES') AS CHAR), 'ck_command_actor_mode,ck_command_c1_assignment_null,ck_command_completion,ck_command_retention,ck_command_state');

CALL c1_assert('schema.batch_audit_events_enforced_checks', CAST((SELECT GROUP_CONCAT(CONSTRAINT_NAME ORDER BY CONSTRAINT_NAME) FROM information_schema.table_constraints WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME='batch_audit_events' AND CONSTRAINT_TYPE='CHECK' AND ENFORCED='YES') AS CHAR), 'ck_batch_audit_actor_mode,ck_batch_audit_c1_assignment_null,ck_batch_audit_c1_claim_null,ck_batch_audit_from_state,ck_batch_audit_sequence,ck_batch_audit_to_state,ck_batch_audit_version');

CALL c1_assert('schema.event_outbox_enforced_checks', CAST((SELECT GROUP_CONCAT(CONSTRAINT_NAME ORDER BY CONSTRAINT_NAME) FROM information_schema.table_constraints WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME='event_outbox' AND CONSTRAINT_TYPE='CHECK' AND ENFORCED='YES') AS CHAR), 'ck_outbox_aggregate_version,ck_outbox_attempts,ck_outbox_delivery_metadata,ck_outbox_key,ck_outbox_schema_version,ck_outbox_sequence,ck_outbox_state');

CALL c1_assert('index.idx_batches_org_status', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND INDEX_NAME='idx_batches_org_status') AS CHAR), '1:organization_id,status,created_at,id');

CALL c1_assert('index.idx_batches_org_created', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND INDEX_NAME='idx_batches_org_created') AS CHAR), '1:organization_id,created_at,id');

CALL c1_assert('index.idx_batches_creator', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='ewaste_batches' AND INDEX_NAME='idx_batches_creator') AS CHAR), '1:created_by');

CALL c1_assert('index.uq_command_replay', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='command_idempotency' AND INDEX_NAME='uq_command_replay') AS CHAR), '0:actor_scope,command_name,idempotency_key');

CALL c1_assert('index.uq_batch_audit_command_seq', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='batch_audit_events' AND INDEX_NAME='uq_batch_audit_command_seq') AS CHAR), '0:command_id,sequence_in_command');

CALL c1_assert('index.uq_outbox_command_event', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='event_outbox' AND INDEX_NAME='uq_outbox_command_event') AS CHAR), '0:command_id,batch_id,event_type');

CALL c1_assert('index.uq_outbox_command_seq', CAST((SELECT CONCAT(MIN(NON_UNIQUE), ':', GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)) FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='event_outbox' AND INDEX_NAME='uq_outbox_command_seq') AS CHAR), '0:command_id,sequence_in_command');

CALL c1_assert('schema.restrictive_foreign_keys', CAST((SELECT COUNT(*) FROM information_schema.referential_constraints WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME IN ('ewaste_batches','command_idempotency','batch_audit_events','event_outbox') AND DELETE_RULE IN ('RESTRICT','NO ACTION') AND UPDATE_RULE IN ('RESTRICT','NO ACTION')) AS CHAR), '10');

CALL c1_assert('schema.no_later_slice_tables', CAST((SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN ('batch_claims','capacity_reservations','batch_assignments','batch_handoffs','matching_decisions')) AS CHAR), '0');

-- Persisted fixtures and complete command/audit/outbox relationships.

CALL c1_assert('fixture.ewaste_batches_count', CAST((SELECT COUNT(*) FROM ewaste_batches) AS CHAR), '7');

CALL c1_assert('fixture.command_idempotency_count', CAST((SELECT COUNT(*) FROM command_idempotency) AS CHAR), '12');

CALL c1_assert('fixture.batch_audit_events_count', CAST((SELECT COUNT(*) FROM batch_audit_events) AS CHAR), '12');

CALL c1_assert('fixture.event_outbox_count', CAST((SELECT COUNT(*) FROM event_outbox) AS CHAR), '5');

CALL c1_assert('fixture.minimal_draft', CAST((SELECT COUNT(*) FROM ewaste_batches WHERE id='b1260000-0000-4000-8000-000000000001' AND status='DRAFT' AND version=1 AND claim_epoch=1 AND submitted_at IS NULL AND category IS NULL AND quantity IS NULL AND estimated_weight_kg IS NULL AND condition_rating IS NULL AND zone IS NULL AND collection_deadline IS NULL AND notes IS NULL AND is_data_bearing=0) AS CHAR), '1');

CALL c1_assert('fixture.partial_draft', CAST((SELECT COUNT(*) FROM ewaste_batches WHERE id='b1260000-0000-4000-8000-000000000002' AND status='DRAFT' AND category='BATTERIES' AND quantity=1 AND estimated_weight_kg IS NULL AND condition_rating IS NULL AND zone='WEST' AND is_data_bearing=1 AND submitted_at IS NULL) AS CHAR), '1');

CALL c1_assert('fixture.minimum_boundaries', CAST((SELECT COUNT(*) FROM ewaste_batches WHERE quantity=1 AND estimated_weight_kg=0.10 AND collection_deadline=submitted_at+INTERVAL 48 HOUR AND status='SUBMITTED') AS CHAR), '1');

CALL c1_assert('fixture.maximum_boundaries', CAST((SELECT COUNT(*) FROM ewaste_batches WHERE quantity=100000 AND estimated_weight_kg=50000.00 AND collection_deadline=submitted_at+INTERVAL 90 DAY AND CHAR_LENGTH(notes)=500 AND OCTET_LENGTH(notes)=1500) AS CHAR), '1');

CALL c1_assert('fixture.all_categories', CAST((SELECT COUNT(DISTINCT category) FROM ewaste_batches WHERE status='SUBMITTED') AS CHAR), '4');

CALL c1_assert('fixture.all_conditions', CAST((SELECT COUNT(DISTINCT condition_rating) FROM ewaste_batches WHERE status='SUBMITTED') AS CHAR), '3');

CALL c1_assert('fixture.all_zones', CAST((SELECT COUNT(DISTINCT zone) FROM ewaste_batches WHERE status='SUBMITTED') AS CHAR), '5');

CALL c1_assert('fixture.all_booleans', CAST((SELECT COUNT(DISTINCT is_data_bearing) FROM ewaste_batches WHERE status='SUBMITTED') AS CHAR), '2');

CALL c1_assert('fixture.exact_decimal_roundtrip', CAST((SELECT CAST(estimated_weight_kg AS CHAR) FROM ewaste_batches WHERE id='b1260000-0000-4000-8000-000000000005') AS CHAR), '12.34');

CALL c1_assert('fixture.null_claim_assignment', CAST((SELECT COUNT(*) FROM ewaste_batches WHERE current_claim_id IS NULL AND current_assignment_id IS NULL AND claim_epoch=1) AS CHAR), '7');

CALL c1_assert('fixture.donor_membership', CAST((SELECT COUNT(*) FROM ewaste_batches b JOIN users u ON u.user_id=b.created_by AND u.organisation_id=b.organization_id WHERE u.role_code='DONOR' AND u.status='ACTIVE') AS CHAR), '7');

CALL c1_assert('fixture.submission_atomic_links', CAST((SELECT COUNT(*) FROM ewaste_batches b JOIN command_idempotency c ON c.batch_id=b.id AND c.command_name='SubmitBatch' JOIN batch_audit_events a ON a.command_id=c.id AND a.batch_id=b.id AND a.actor_user_id=b.created_by AND a.actor_org_id=b.organization_id JOIN event_outbox e ON e.command_id=c.id AND e.batch_id=b.id WHERE b.status='SUBMITTED' AND b.version=2 AND c.state='COMPLETED' AND c.response_status=200 AND JSON_UNQUOTE(JSON_EXTRACT(c.response_json,'$.eventId'))=e.event_id AND a.event_type='RequestSubmitted' AND a.from_status='DRAFT' AND a.to_status='SUBMITTED' AND a.batch_version=b.version AND e.aggregate_version=b.version AND e.publish_state='PENDING' AND e.occurred_at=b.submitted_at AND e.correlation_id=a.correlation_id) AS CHAR), '5');

CALL c1_assert('fixture.draft_history', CAST((SELECT COUNT(*) FROM batch_audit_events WHERE event_type='DraftSaved' AND from_status='DRAFT' AND to_status='DRAFT' AND batch_version=1 AND JSON_UNQUOTE(JSON_EXTRACT(details_json,'$.operation'))='CREATE') AS CHAR), '7');

CALL c1_assert('fixture.no_draft_kafka_event', CAST((SELECT COUNT(*) FROM event_outbox e JOIN ewaste_batches b ON b.id=e.batch_id WHERE b.status='DRAFT') AS CHAR), '0');

CALL c1_assert('fixture.event_envelope_and_payload_types', CAST((SELECT COUNT(*) FROM event_outbox e WHERE JSON_LENGTH(payload_json)=11 AND JSON_LENGTH(JSON_EXTRACT(payload_json,'$.data'))=9 AND JSON_TYPE(JSON_EXTRACT(payload_json,'$.claim_epoch'))='STRING' AND JSON_TYPE(JSON_EXTRACT(payload_json,'$.batch_version')) IN ('INTEGER','UNSIGNED INTEGER') AND JSON_TYPE(JSON_EXTRACT(payload_json,'$.data.quantity')) IN ('INTEGER','UNSIGNED INTEGER') AND JSON_TYPE(JSON_EXTRACT(payload_json,'$.data.estimated_weight_kg'))='STRING' AND JSON_TYPE(JSON_EXTRACT(payload_json,'$.data.is_data_bearing'))='BOOLEAN' AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.event_id'))=event_id AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.command_id'))=command_id AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.batch_id'))=batch_id AND partition_key=batch_id AND JSON_EXTRACT(payload_json,'$.data.notes') IS NULL AND JSON_EXTRACT(payload_json,'$.data.actor_user_id') IS NULL) AS CHAR), '5');

-- Each invalid INSERT must fail for the expected constraint, not another error.

CALL c1_clone('reject.batch.unknown_status', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('status', '''NO_MATCH'''), 3819, 'ck_batches_status');

CALL c1_clone('reject.batch.status_case', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('status', '''submitted'''), 3819, 'ck_batches_status');

CALL c1_clone('reject.batch.unknown_category', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('category', '''UNKNOWN'''), 3819, 'ck_batches_category');

CALL c1_clone('reject.batch.category_case', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('category', '''ict_equipment'''), 3819, 'ck_batches_category');

CALL c1_clone('reject.batch.quantity_zero', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('quantity', '0'), 3819, 'ck_batches_quantity');

CALL c1_clone('reject.batch.quantity_above_max', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('quantity', '100001'), 3819, 'ck_batches_quantity');

CALL c1_clone('reject.batch.quantity_negative', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('quantity', '-1'), 1264, 'quantity');

CALL c1_clone('reject.batch.weight_below_min', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('estimated_weight_kg', '0.09'), 3819, 'ck_batches_weight');

CALL c1_clone('reject.batch.weight_above_max', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('estimated_weight_kg', '50000.01'), 3819, 'ck_batches_weight');

CALL c1_clone('reject.batch.unknown_condition', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('condition_rating', '''BROKEN'''), 3819, 'ck_batches_condition');

CALL c1_clone('reject.batch.condition_case', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('condition_rating', '''functional'''), 3819, 'ck_batches_condition');

CALL c1_clone('reject.batch.non_boolean', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('is_data_bearing', '2'), 3819, 'ck_batches_data_bearing');

CALL c1_clone('reject.batch.null_boolean', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('is_data_bearing', 'NULL'), 1048, 'is_data_bearing');

CALL c1_clone('reject.batch.unknown_zone', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('zone', '''REMOTE'''), 3819, 'ck_batches_zone');

CALL c1_clone('reject.batch.zone_case', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('zone', '''north'''), 3819, 'ck_batches_zone');

CALL c1_clone('reject.batch.deadline_1us_early', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('collection_deadline', '''2026-09-03 00:59:59.999999'''), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.deadline_1us_late', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('collection_deadline', '''2026-11-30 01:00:00.000001'''), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.notes_501_characters', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('notes', 'REPEAT(''界'',501)'), 1406, 'notes');

CALL c1_clone('reject.batch.epoch_zero', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('claim_epoch', '0'), 3819, 'ck_batches_epoch');

CALL c1_clone('reject.batch.version_zero', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('version', '0'), 3819, 'ck_batches_version');

CALL c1_clone('reject.batch.updated_before_created', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('updated_at', '''2026-08-31 23:59:59.999999'''), 3819, 'ck_batches_updated_time');

CALL c1_clone('reject.batch.submitted_before_created', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('submitted_at', '''2026-08-31 23:59:59.999999'''), 3819, 'ck_batches_submitted_time');

CALL c1_clone('reject.batch.claim_pointer', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('current_claim_id', '''c1269999-0000-4000-8000-000000000001'''), 3819, 'ck_batches_c1_claim_null');

CALL c1_clone('reject.batch.assignment_pointer', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('current_assignment_id', '''a1269999-0000-4000-8000-000000000001'''), 3819, 'ck_batches_c1_assignment_null');

CALL c1_clone('reject.batch.missing_organisation', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('organization_id', '''NONEXISTENT'''), 1452, 'fk_batches_organisation');

CALL c1_clone('reject.batch.missing_creator', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('created_by', '''NONEXISTENT'''), 1452, 'fk_batches_creator');

CALL c1_clone('reject.batch.null_organisation', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('organization_id', 'NULL'), 1048, 'organization_id');

CALL c1_clone('reject.batch.null_creator', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('created_by', 'NULL'), 1048, 'created_by');

CALL c1_clone('reject.batch.missing_category', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('category', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.missing_quantity', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('quantity', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.missing_estimated_weight_kg', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('estimated_weight_kg', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.missing_condition_rating', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('condition_rating', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.missing_zone', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('zone', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.missing_collection_deadline', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('collection_deadline', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.missing_submitted_at', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('submitted_at', 'NULL'), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.draft_has_submission_time', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('status', '''DRAFT'''), 3819, 'ck_batches_submit_completeness');

CALL c1_clone('reject.batch.invalid_partial_draft', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000002', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('quantity', '0'), 3819, 'ck_batches_quantity');

CALL c1_clone('reject.batch.duplicate_id', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1260000-0000-4000-8000-000000000003',
    JSON_OBJECT(), 1062, 'PRIMARY');

CALL c1_clone('valid.batch.submitted', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT(), 0, '');

CALL c1_clone('valid.batch.partial_draft', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000002', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT(), 0, '');

CALL c1_clone('valid.batch.full_u64_epoch', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('claim_epoch', '18446744073709551615'), 0, '');

CALL c1_clone('valid.batch.full_u32_version', 'ewaste_batches', 'b1260000-0000-4000-8000-000000000003', 'b1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('version', '4294967295'), 0, '');

-- Command state, scope, case sensitivity and referential constraints.

CALL c1_clone('valid.command.valid_same_key_different_actor', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('actor_scope', '''user:USR-004''', 'actor_user_id', '''USR-004'''), 0, '');

CALL c1_clone('valid.command.valid_same_key_different_command', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_name', '''CreateDraft'''), 0, '');

CALL c1_clone('valid.command.valid_key_case_distinct', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', 'UPPER(b.idempotency_key)'), 0, '');

CALL c1_clone('reject.command.duplicate_scope_key', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT(), 1062, 'uq_command_replay');

CALL c1_clone('reject.command.missing_actor', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'actor_user_id', 'NULL'), 3819, 'ck_command_actor_mode');

CALL c1_clone('reject.command.two_actor_modes', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'service_principal', '''service:test'''), 3819, 'ck_command_actor_mode');

-- Unknown state violates both the state enum and completion-shape checks.
CALL c1_clone('reject.command.invalid_state', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'state', '''PREPARED'''), 3819, 'ck_command_completion');

CALL c1_clone('reject.command.completed_without_response', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'response_json', 'NULL'), 3819, 'ck_command_completion');

CALL c1_clone('reject.command.completed_without_status', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'response_status', 'NULL'), 3819, 'ck_command_completion');

CALL c1_clone('reject.command.completed_without_time', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'completed_at', 'NULL'), 3819, 'ck_command_completion');

CALL c1_clone('reject.command.completed_before_created', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'completed_at', '''2026-09-01 00:59:59.999999'''), 3819, 'ck_command_completion');

CALL c1_clone('reject.command.retention_before_completion', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'retain_until', '''2026-09-01 00:00:00.000000'''), 3819, 'ck_command_completion');

CALL c1_clone('reject.command.assignment_pointer', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'assignment_id', '''a1269999-0000-4000-8000-000000000001'''), 3819, 'ck_command_c1_assignment_null');

CALL c1_clone('reject.command.missing_user_fk', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'actor_user_id', '''UNKNOWN'''), 1452, 'fk_command_actor');

CALL c1_clone('reject.command.missing_batch_fk', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'batch_id', '''b1269999-0000-4000-8000-000000000001'''), 1452, 'fk_command_batch');

CALL c1_clone('valid.command.valid_in_progress', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'state', '''IN_PROGRESS''', 'response_status', 'NULL', 'response_json', 'JSON_OBJECT(''prepared'',true)', 'completed_at', 'NULL'), 0, '');

CALL c1_clone('valid.command.valid_service_actor', 'command_idempotency', 'c1260001-0000-4000-8000-000000000003', 'c1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('idempotency_key', '''c1-test-new-key''', 'actor_user_id', 'NULL', 'service_principal', '''test:fixture-worker''', 'actor_scope', '''service:fixture-worker'''), 0, '');

CALL c1_clone('reject.audit.duplicate_command_sequence', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT(), 1062, 'uq_batch_audit_command_seq');

CALL c1_clone('valid.audit.valid_human', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2'), 0, '');

CALL c1_clone('valid.audit.valid_service', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'actor_user_id', 'NULL', 'actor_org_id', 'NULL', 'service_principal', '''test:fixture-worker'''), 0, '');

CALL c1_clone('reject.audit.missing_org', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'actor_org_id', 'NULL'), 3819, 'ck_batch_audit_actor_mode');

CALL c1_clone('reject.audit.missing_user', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'actor_user_id', 'NULL'), 3819, 'ck_batch_audit_actor_mode');

CALL c1_clone('reject.audit.mixed_actors', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'service_principal', '''test:fixture-worker'''), 3819, 'ck_batch_audit_actor_mode');

CALL c1_clone('reject.audit.unknown_from', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'from_status', '''NO_MATCH'''), 3819, 'ck_batch_audit_from_state');

CALL c1_clone('reject.audit.unknown_to', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'to_status', '''NO_MATCH'''), 3819, 'ck_batch_audit_to_state');

CALL c1_clone('reject.audit.zero_version', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'batch_version', '0'), 3819, 'ck_batch_audit_version');

CALL c1_clone('reject.audit.zero_sequence', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '0'), 3819, 'ck_batch_audit_sequence');

CALL c1_clone('reject.audit.claim_pointer', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'claim_id', '''c1269999-0000-4000-8000-000000000001'''), 3819, 'ck_batch_audit_c1_claim_null');

CALL c1_clone('reject.audit.assignment_pointer', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'assignment_id', '''a1269999-0000-4000-8000-000000000001'''), 3819, 'ck_batch_audit_c1_assignment_null');

CALL c1_clone('reject.audit.unknown_batch', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'batch_id', '''b1269999-0000-4000-8000-000000000001'''), 1452, 'fk_batch_audit_batch');

CALL c1_clone('reject.audit.unknown_command', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'command_id', '''c1269999-0000-4000-8000-000000000001'''), 1452, 'fk_batch_audit_command');

CALL c1_clone('reject.audit.unknown_user', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'actor_user_id', '''UNKNOWN'''), 1452, 'fk_batch_audit_actor');

CALL c1_clone('reject.audit.unknown_org', 'batch_audit_events', 'a1260001-0000-4000-8000-000000000003', 'a1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2', 'actor_org_id', '''UNKNOWN'''), 1452, 'fk_batch_audit_org');

CALL c1_clone('valid.outbox.valid_pending', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003'''), 0, '');

CALL c1_clone('valid.outbox.valid_published', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'publish_state', '''PUBLISHED''', 'next_attempt_at', 'NULL', 'published_at', '''2026-09-01 01:00:01.000000'''), 0, '');

CALL c1_clone('valid.outbox.valid_quarantined', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'publish_state', '''QUARANTINED''', 'next_attempt_at', 'NULL'), 0, '');

CALL c1_clone('reject.outbox.duplicate_command_event', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('sequence_in_command', '2'), 1062, 'uq_outbox_command_event');

CALL c1_clone('reject.outbox.duplicate_command_sequence', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('event_type', '''OtherTestEvent'''), 1062, 'uq_outbox_command_seq');

CALL c1_clone('reject.outbox.wrong_partition_key', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'partition_key', '''b1260000-0000-4000-8000-000000000004'''), 3819, 'ck_outbox_key');

-- Unknown state also violates the delivery-metadata shape check.
CALL c1_clone('reject.outbox.invalid_state', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'publish_state', '''SENT'''), 3819, 'ck_outbox_delivery_metadata');

CALL c1_clone('reject.outbox.zero_schema_version', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'schema_version', '0'), 3819, 'ck_outbox_schema_version');

CALL c1_clone('reject.outbox.zero_aggregate_version', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'aggregate_version', '0'), 3819, 'ck_outbox_aggregate_version');

CALL c1_clone('reject.outbox.zero_sequence', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'sequence_in_command', '0'), 3819, 'ck_outbox_sequence');

CALL c1_clone('reject.outbox.pending_without_next_attempt', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'next_attempt_at', 'NULL'), 3819, 'ck_outbox_delivery_metadata');

CALL c1_clone('reject.outbox.pending_with_published_at', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'published_at', '''2026-09-01 01:00:01.000000'''), 3819, 'ck_outbox_delivery_metadata');

CALL c1_clone('reject.outbox.published_without_time', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'publish_state', '''PUBLISHED''', 'next_attempt_at', 'NULL'), 3819, 'ck_outbox_delivery_metadata');

CALL c1_clone('reject.outbox.quarantined_with_next_attempt', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'publish_state', '''QUARANTINED'''), 3819, 'ck_outbox_delivery_metadata');

CALL c1_clone('reject.outbox.unknown_command', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1269999-0000-4000-8000-000000000001'''), 1452, 'fk_outbox_command');

CALL c1_clone('reject.outbox.unknown_batch', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'batch_id', '''b1269999-0000-4000-8000-000000000001''', 'partition_key', '''b1269999-0000-4000-8000-000000000001'''), 1452, 'fk_outbox_batch');

CALL c1_clone('reject.outbox.invalid_json', 'event_outbox', 'e1260000-0000-4000-8000-000000000003', 'e1269999-0000-4000-8000-000000000099',
    JSON_OBJECT('command_id', '''c1260000-0000-4000-8000-000000000003''', 'payload_json', '''{invalid'''), 3140, '');

CALL c1_statement('reject.delete.referenced_batch', 'DELETE FROM ewaste_batches WHERE id=''b1260000-0000-4000-8000-000000000003''', 1451, 'fk_');

CALL c1_statement('reject.delete.referenced_command', 'DELETE FROM command_idempotency WHERE id=''c1260001-0000-4000-8000-000000000003''', 1451, 'fk_');

CALL c1_statement('reject.delete.referenced_organisation', 'DELETE FROM organisations WHERE organisation_id=''DON-001''', 1451, 'fk_');

-- Defaults and updates are read back before rollback; SQL coercion is not API validation.
START TRANSACTION;
INSERT INTO ewaste_batches(id, organization_id, created_by)
VALUES('b1269999-0000-4000-8000-000000000098', 'DON-001', 'USR-003');
CALL c1_assert('valid.batch.database_defaults',
    (SELECT CONCAT_WS('/',status,version,claim_epoch,is_data_bearing,
        submitted_at IS NULL,current_claim_id IS NULL,current_assignment_id IS NULL,
        created_at IS NOT NULL,updated_at>=created_at)
     FROM ewaste_batches WHERE id='b1269999-0000-4000-8000-000000000098'),
    'DRAFT/1/1/0/1/1/1/1/1');
UPDATE ewaste_batches SET category='BATTERIES', quantity=2, estimated_weight_kg=1.23,
    version=version+1 WHERE id='b1269999-0000-4000-8000-000000000098';
CALL c1_assert('valid.batch.draft_edit_roundtrip',
    (SELECT CONCAT_WS('/',category,quantity,estimated_weight_kg,version,status)
     FROM ewaste_batches WHERE id='b1269999-0000-4000-8000-000000000098'), 'BATTERIES/2/1.23/2/DRAFT');
ROLLBACK;

DELIMITER $$
CREATE PROCEDURE c1_transaction(IN p_fail BOOLEAN)
BEGIN
    DECLARE observed INT DEFAULT 0;
    DECLARE message_value TEXT DEFAULT '';
    START TRANSACTION;
    BEGIN
        DECLARE EXIT HANDLER FOR SQLEXCEPTION
            GET DIAGNOSTICS CONDITION 1 observed=MYSQL_ERRNO, message_value=MESSAGE_TEXT;
        INSERT INTO ewaste_batches(id, organization_id, created_by, category, quantity,
            estimated_weight_kg, condition_rating, is_data_bearing, zone, collection_deadline,
            created_at, updated_at)
        VALUES('b1269999-0000-4000-8000-000000000090','DON-001','USR-003','BATTERIES',2,
            1.23,'REPAIRABLE',FALSE,'NORTH','2026-09-03 01:00:00.000000',
            '2026-09-01 00:00:00.000000','2026-09-01 00:00:00.000000');
        INSERT INTO command_idempotency(id,actor_user_id,actor_scope,command_name,
            idempotency_key,request_hash,batch_id,created_at,retain_until)
        VALUES('c1269999-0000-4000-8000-000000000090','USR-003','user:USR-003','FixtureSubmit',
            'c1-transaction',SHA2('c1-transaction',256),'b1269999-0000-4000-8000-000000000090',
            '2026-09-01 01:00:00.000000','2027-09-01 00:00:00.000000');
        UPDATE ewaste_batches SET status='SUBMITTED',version=version+1,
            submitted_at='2026-09-01 01:00:00.000000',updated_at='2026-09-01 01:00:00.000000'
        WHERE id='b1269999-0000-4000-8000-000000000090' AND status='DRAFT' AND version=1;
        INSERT INTO batch_audit_events(id,batch_id,command_id,actor_user_id,actor_org_id,event_type,
            from_status,to_status,batch_version,sequence_in_command,occurred_at,correlation_id,details_json)
        VALUES('a1269999-0000-4000-8000-000000000090','b1269999-0000-4000-8000-000000000090',
            'c1269999-0000-4000-8000-000000000090','USR-003','DON-001','RequestSubmitted',
            'DRAFT','SUBMITTED',2,1,'2026-09-01 01:00:00.000000','c1-transaction',JSON_OBJECT('operation','SUBMIT'));
        INSERT INTO event_outbox(event_id,batch_id,command_id,event_type,topic,schema_version,
            aggregate_version,sequence_in_command,partition_key,payload_json,correlation_id,
            occurred_at,created_at,next_attempt_at)
        SELECT 'e1269999-0000-4000-8000-000000000090','b1269999-0000-4000-8000-000000000090',
            'c1269999-0000-4000-8000-000000000090',event_type,topic,schema_version,2,1,
            IF(p_fail,'wrong-key','b1269999-0000-4000-8000-000000000090'),
            JSON_SET(payload_json,'$.event_id','e1269999-0000-4000-8000-000000000090',
                '$.command_id','c1269999-0000-4000-8000-000000000090',
                '$.batch_id','b1269999-0000-4000-8000-000000000090',
                '$.correlation_id','c1-transaction','$.data.category','BATTERIES',
                '$.data.quantity',2,'$.data.estimated_weight_kg','1.23',
                '$.data.condition_rating','REPAIRABLE'),
            'c1-transaction',occurred_at,created_at,next_attempt_at
        FROM event_outbox WHERE event_id='e1260000-0000-4000-8000-000000000003';
        UPDATE command_idempotency SET state='COMPLETED',response_status=200,
            response_json=JSON_OBJECT('batchId','b1269999-0000-4000-8000-000000000090',
                'status','SUBMITTED','version',2,'eventId','e1269999-0000-4000-8000-000000000090'),
            completed_at='2026-09-01 01:00:00.000000'
        WHERE id='c1269999-0000-4000-8000-000000000090';
    END;
    IF observed<>0 OR p_fail THEN ROLLBACK; ELSE COMMIT; END IF;
    CALL c1_assert(IF(p_fail,'transaction.forced_failure','transaction.success'),
        CAST(observed AS CHAR), IF(p_fail,'3819','0'));
    IF p_fail THEN
        CALL c1_assert('transaction.failure_is_outbox_constraint',
            CAST(LOCATE('ck_outbox_key',message_value)>0 AS CHAR),'1');
    END IF;
END$$
DELIMITER ;
CALL c1_transaction(TRUE);

CALL c1_assert('transaction.rollback_ewaste_batches', CAST((SELECT COUNT(*) FROM ewaste_batches) AS CHAR), '7');

CALL c1_assert('transaction.rollback_command_idempotency', CAST((SELECT COUNT(*) FROM command_idempotency) AS CHAR), '12');

CALL c1_assert('transaction.rollback_batch_audit_events', CAST((SELECT COUNT(*) FROM batch_audit_events) AS CHAR), '12');

CALL c1_assert('transaction.rollback_event_outbox', CAST((SELECT COUNT(*) FROM event_outbox) AS CHAR), '5');

CALL c1_transaction(FALSE);

CALL c1_assert('transaction.committed_all_four_tables', CAST((SELECT COUNT(*) FROM ewaste_batches b JOIN command_idempotency c ON c.batch_id=b.id JOIN batch_audit_events a ON a.command_id=c.id AND a.batch_id=b.id JOIN event_outbox e ON e.command_id=c.id AND e.batch_id=b.id WHERE b.id='b1269999-0000-4000-8000-000000000090' AND b.status='SUBMITTED' AND b.version=2 AND c.state='COMPLETED' AND c.response_status=200 AND JSON_UNQUOTE(JSON_EXTRACT(c.response_json,'$.eventId'))=e.event_id AND a.batch_version=2 AND e.aggregate_version=2 AND e.publish_state='PENDING') AS CHAR), '1');

CALL c1_assert('transaction.final_ewaste_batches', CAST((SELECT COUNT(*) FROM ewaste_batches) AS CHAR), '8');

CALL c1_assert('transaction.final_command_idempotency', CAST((SELECT COUNT(*) FROM command_idempotency) AS CHAR), '13');

CALL c1_assert('transaction.final_batch_audit_events', CAST((SELECT COUNT(*) FROM batch_audit_events) AS CHAR), '13');

CALL c1_assert('transaction.final_event_outbox', CAST((SELECT COUNT(*) FROM event_outbox) AS CHAR), '6');

SELECT test_name,result,actual,expected FROM c1_results ORDER BY sequence_id;
CALL c1_finish();
DROP PROCEDURE c1_transaction;
DROP PROCEDURE c1_clone;
DROP PROCEDURE c1_statement;
DROP PROCEDURE c1_assert;
DROP PROCEDURE c1_finish;
DROP TEMPORARY TABLE c1_results;
PERSISTENCE_EMBED_005

  mkdir -p "$WORK_DIR/database/tests/c3"
  # Embedded database/tests/c3/changelog-c3.yaml
  cat > "$WORK_DIR/database/tests/c3/changelog-c3.yaml" <<'PERSISTENCE_EMBED_006'
# Frozen C3 test boundary; original Liquibase file identities are preserved.
databaseChangeLog:
  - include:
      file: changes/001-create-organisations.sql
  - include:
      file: changes/002-create-roles.sql
  - include:
      file: changes/003-create-users.sql
  - include:
      file: changes/004-create-sessions.sql
  - include:
      file: changes/005-create-login-audit.sql
  - include:
      file: changes/006-create-ewaste-batches.sql
  - include:
      file: changes/007-add-ewaste-batch-constraints.sql
  - include:
      file: changes/008-create-command-idempotency.sql
  - include:
      file: changes/009-create-batch-audit-events.sql
  - include:
      file: changes/010-create-event-outbox.sql
  - include:
      file: changes/011-create-matching-rule-sets.sql
  - include:
      file: changes/012-create-recycler-matching-profiles.sql
  - include:
      file: changes/013-create-recycler-capacity-pools.sql
  - include:
      file: changes/014-create-recycler-category-capabilities.sql
  - include:
      file: changes/015-create-recycler-service-zones.sql
  - include:
      file: changes/016-create-matching-decisions.sql
  - include:
      file: changes/017-create-matched-results.sql
  - include:
      file: changes/018-create-batch-claims.sql
  - include:
      file: changes/019-create-capacity-reservations.sql
  - include:
      file: changes/020-link-current-claim-and-audit.sql
  - include:
      file: seed/101-seed-organisations.sql
  - include:
      file: seed/102-seed-roles.sql
  - include:
      file: seed/103-seed-users.sql
  - include:
      file: seed/104-seed-c1-batches.sql
PERSISTENCE_EMBED_006

  mkdir -p "$WORK_DIR/database/tests/c3"
  # Embedded database/tests/c3/schema-fixtures.sql
  cat > "$WORK_DIR/database/tests/c3/schema-fixtures.sql" <<'PERSISTENCE_EMBED_007'
-- Test-only, deterministic C3 records. Requires migrations 001-020 and @seed.
-- Run once in a disposable database. These are row-constraint fixtures, not
-- evidence of an application command, matching execution or authentication.
SET NAMES utf8mb4;
SET time_zone = '+00:00';
START TRANSACTION;
INSERT INTO recycler_capacity_pools
    (id, recycler_org_id, pool_code, total_kg, reserved_kg, is_active, version, updated_at)
VALUES
    ('d3000000-0000-4000-8000-000000000001', 'PROC-001', 'C3-CONSTRAINTS', 1000, 100, 1, 2, '2026-09-18 10:00:00.000000');

INSERT INTO ewaste_batches
    (id, organization_id, created_by, status, category, quantity,
     estimated_weight_kg, condition_rating, is_data_bearing, zone,
     collection_deadline, claim_epoch, version, submitted_at, created_at, updated_at)
VALUES
    ('b3000000-0000-4000-8000-000000000001', 'DON-001', 'USR-003', 'MATCHED',
     'ICT_EQUIPMENT', 5, 100, 'REPAIRABLE', 1, 'CENTRAL', '2026-09-25 10:00:00.000000',
     1, 3, '2026-09-17 10:00:00.000000', '2026-09-17 09:00:00.000000', '2026-09-18 09:00:00.000000'),
    ('b3000000-0000-4000-8000-000000000002', 'DON-001', 'USR-003', 'MATCHED',
     'ICT_EQUIPMENT', 5, 100, 'REPAIRABLE', 1, 'CENTRAL', '2026-09-25 10:00:00.000000',
     1, 3, '2026-09-17 10:00:00.000000', '2026-09-17 09:00:00.000000', '2026-09-18 09:00:00.000000');

INSERT INTO batch_claims
    (id, batch_id, claim_epoch, recycler_org_id, claimed_by, claim_status,
     idempotency_key, claimed_at, created_at)
VALUES
    ('f3000000-0000-4000-8000-000000000001', 'b3000000-0000-4000-8000-000000000001',
     1, 'PROC-001', 'USR-007', 'ACCEPTED', 'C3-Schema-Winner-0001',
     '2026-09-18 10:00:00.000000', '2026-09-18 10:00:00.000000');

INSERT INTO capacity_reservations
    (id, batch_id, claim_id, capacity_pool_id, reserved_kg, status, reserved_at, version)
VALUES
    ('a3000000-0000-4000-8000-000000000001', 'b3000000-0000-4000-8000-000000000001',
     'f3000000-0000-4000-8000-000000000001', 'd3000000-0000-4000-8000-000000000001',
     100, 'RESERVED', '2026-09-18 10:00:00.000000', 1);

UPDATE ewaste_batches SET status='APPROVED', version=4,
    current_claim_id='f3000000-0000-4000-8000-000000000001',
    updated_at='2026-09-18 10:00:00.000000'
WHERE id='b3000000-0000-4000-8000-000000000001';

INSERT INTO command_idempotency
    (id, actor_user_id, actor_scope, command_name, idempotency_key, request_hash,
     batch_id, state, response_status, response_json, created_at, completed_at, retain_until)
VALUES
    ('c3000000-0000-4000-8000-000000000001', 'USR-007', 'user:USR-007',
     'ClaimOpportunity', 'C3-Schema-Winner-0001', SHA2('schema-fixture-only',256),
     'b3000000-0000-4000-8000-000000000001', 'COMPLETED', 200,
     JSON_OBJECT('claim_id','f3000000-0000-4000-8000-000000000001',
                 'batch_id','b3000000-0000-4000-8000-000000000001',
                 'recycler_org_id','PROC-001','claim_epoch','1','status','ACCEPTED',
                 'batch_status','APPROVED','claimed_at','2026-09-18T10:00:00.000000Z',
                 'correlation_id','c3-schema-fixture'),
     '2026-09-18 10:00:00.000000','2026-09-18 10:00:00.000000','2027-09-18 10:00:00.000000');

INSERT INTO batch_audit_events
    (id, batch_id, command_id, claim_id, actor_user_id, actor_org_id, event_type,
     from_status, to_status, batch_version, sequence_in_command, occurred_at,
     correlation_id, details_json)
VALUES
    ('e3000000-0000-4000-8000-000000000001','b3000000-0000-4000-8000-000000000001',
     'c3000000-0000-4000-8000-000000000001','f3000000-0000-4000-8000-000000000001',
     'USR-007','PROC-001','ClaimConfirmed','MATCHED','APPROVED',4,1,
     '2026-09-18 10:00:00.000000','c3-schema-fixture',JSON_OBJECT('fixture','row-constraints'));

INSERT INTO event_outbox
    (event_id,batch_id,command_id,event_type,topic,schema_version,aggregate_version,
     sequence_in_command,partition_key,payload_json,correlation_id,occurred_at,
     created_at,publish_state,attempt_count,next_attempt_at)
VALUES
    ('e3000001-0000-4000-8000-000000000001','b3000000-0000-4000-8000-000000000001',
     'c3000000-0000-4000-8000-000000000001','ClaimConfirmed','ewaste.claim.events',1,4,1,
     'b3000000-0000-4000-8000-000000000001',
     JSON_OBJECT('event_id','e3000001-0000-4000-8000-000000000001','event_type','ClaimConfirmed',
       'schema_version',1,'command_id','c3000000-0000-4000-8000-000000000001',
       'batch_id','b3000000-0000-4000-8000-000000000001','batch_version',4,'claim_epoch','1',
       'sequence_in_command',1,'occurred_at','2026-09-18T10:00:00.000000Z',
       'correlation_id','c3-schema-fixture','data',JSON_OBJECT(
         'claim_id','f3000000-0000-4000-8000-000000000001','recycler_org_id','PROC-001',
         'actor_user_id','USR-007','claimed_at','2026-09-18T10:00:00.000000Z')),
     'c3-schema-fixture','2026-09-18 10:00:00.000000','2026-09-18 10:00:00.000000',
     'PENDING',0,'2026-09-18 10:00:00.000000');
COMMIT;
PERSISTENCE_EMBED_007

  mkdir -p "$WORK_DIR/database/tests/c3"
  # Embedded database/tests/c3/verify-schema.sql
  cat > "$WORK_DIR/database/tests/c3/verify-schema.sql" <<'PERSISTENCE_EMBED_008'
-- C3 runtime checks. Run only in the runner's disposable MySQL database.
-- All mutation checks roll back; load schema-fixtures.sql first.
-- Expected errors include the exact MySQL error number and named constraint.
SET NAMES utf8mb4;
SET time_zone = '+00:00';
SET SESSION group_concat_max_len = 16384;
CREATE TEMPORARY TABLE c3_results (
    sequence_id INT AUTO_INCREMENT PRIMARY KEY,
    test_name VARCHAR(128) NOT NULL UNIQUE,
    result VARCHAR(4) NOT NULL,
    actual VARCHAR(4096),
    expected VARCHAR(4096)
) ENGINE=MEMORY;
DELIMITER $$
CREATE PROCEDURE c3_assert(IN p_name VARCHAR(128), IN p_actual TEXT, IN p_expected TEXT)
BEGIN
    INSERT INTO c3_results(test_name, result, actual, expected)
    VALUES(p_name, IF(BINARY p_actual <=> BINARY p_expected, 'PASS', 'FAIL'), p_actual, p_expected);
END$$
CREATE PROCEDURE c3_statement(
    IN p_name VARCHAR(128), IN p_sql LONGTEXT,
    IN p_errno INT, IN p_constraint VARCHAR(128))
BEGIN
    DECLARE observed INT DEFAULT 0;
    DECLARE message_text_value TEXT DEFAULT '';
    DECLARE affected INT DEFAULT 0;
    DECLARE prepared_ok BOOLEAN DEFAULT FALSE;
    START TRANSACTION;
    BEGIN
        DECLARE EXIT HANDLER FOR SQLEXCEPTION
            GET DIAGNOSTICS CONDITION 1 observed = MYSQL_ERRNO, message_text_value = MESSAGE_TEXT;
        SET @c3_statement = p_sql;
        PREPARE c3_prepared FROM @c3_statement;
        SET prepared_ok = TRUE;
        EXECUTE c3_prepared;
        SET affected = ROW_COUNT();
    END;
    IF prepared_ok THEN DEALLOCATE PREPARE c3_prepared; END IF;
    ROLLBACK;
    INSERT INTO c3_results(test_name, result, actual, expected)
    VALUES(p_name,
        IF(observed = p_errno
           AND (p_errno <> 0 OR affected = 1)
           AND (p_constraint = '' OR LOCATE(p_constraint, message_text_value) > 0), 'PASS', 'FAIL'),
        IF(observed = 0, CONCAT('accepted rows=', affected), CONCAT(observed, ': ', message_text_value)),
        IF(p_errno = 0, 'accepted rows=1', CONCAT(p_errno, ': ', p_constraint)));
END$$
CREATE PROCEDURE c3_clone(
    IN p_name VARCHAR(128), IN p_table VARCHAR(64), IN p_source VARCHAR(36),
    IN p_id VARCHAR(36), IN p_patch JSON, IN p_errno INT, IN p_constraint VARCHAR(128))
BEGIN
    DECLARE columns_sql TEXT;
    DECLARE values_sql TEXT;
    DECLARE pk_name VARCHAR(16);
    IF p_table NOT IN ('batch_claims','capacity_reservations','ewaste_batches','command_idempotency','batch_audit_events','event_outbox') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Invalid test table';
    END IF;
    SET pk_name = IF(p_table = 'event_outbox', 'event_id', 'id');
    SELECT GROUP_CONCAT(CONCAT('`', COLUMN_NAME, '`') ORDER BY ORDINAL_POSITION),
           GROUP_CONCAT(CASE
               WHEN JSON_CONTAINS_PATH(p_patch, 'one', CONCAT('$.', COLUMN_NAME))
                   THEN JSON_UNQUOTE(JSON_EXTRACT(p_patch, CONCAT('$.', COLUMN_NAME)))
               WHEN COLUMN_NAME = pk_name THEN QUOTE(p_id)
               ELSE CONCAT('b.`', COLUMN_NAME, '`') END ORDER BY ORDINAL_POSITION)
    INTO columns_sql, values_sql
    FROM information_schema.columns WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = p_table;
    CALL c3_statement(p_name,
        CONCAT('INSERT INTO ', p_table, ' (', columns_sql, ') SELECT ', values_sql,
               ' FROM ', p_table, ' b WHERE b.', pk_name, ' = ', QUOTE(p_source)),
        p_errno, p_constraint);
END$$
CREATE PROCEDURE c3_finish()
BEGIN
    IF EXISTS(SELECT 1 FROM c3_results WHERE result <> 'PASS') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'C3 persistence check failed';
    END IF;
END$$
DELIMITER ;



CALL c3_assert('schema.batch_claims.columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_claims') AS CHAR), '11');

CALL c3_assert('schema.batch_claims.engine', CAST((SELECT CONCAT(engine,'/',table_collation) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_claims') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c3_assert('schema.capacity_reservations.columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='capacity_reservations') AS CHAR), '11');

CALL c3_assert('schema.capacity_reservations.engine', CAST((SELECT CONCAT(engine,'/',table_collation) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='capacity_reservations') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c3_assert('schema.batch_claims.claim_epoch', CAST((SELECT CONCAT(column_type,'/',is_nullable) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_claims' AND column_name='claim_epoch') AS CHAR), 'bigint unsigned/NO');

CALL c3_assert('schema.capacity_reservations.version', CAST((SELECT CONCAT(column_type,'/',is_nullable) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='capacity_reservations' AND column_name='version') AS CHAR), 'bigint/NO');

CALL c3_assert('schema.capacity_reservations.reserved_kg', CAST((SELECT CONCAT(column_type,'/',is_nullable) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='capacity_reservations' AND column_name='reserved_kg') AS CHAR), 'decimal(8,2)/NO');

CALL c3_assert('collation.batch_claims.idempotency_key', CAST((SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_claims' AND column_name='idempotency_key') AS CHAR), 'utf8mb4_0900_ai_ci');

CALL c3_assert('collation.command_idempotency.idempotency_key', CAST((SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='command_idempotency' AND column_name='idempotency_key') AS CHAR), 'utf8mb4_0900_as_cs');

CALL c3_assert('collation.batch_claims.claim_status', CAST((SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_claims' AND column_name='claim_status') AS CHAR), 'utf8mb4_0900_as_cs');

CALL c3_assert('collation.capacity_reservations.status', CAST((SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='capacity_reservations' AND column_name='status') AS CHAR), 'utf8mb4_0900_as_cs');

CALL c3_assert('unique.uk_batch_claim_epoch', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_claims' AND index_name='uk_batch_claim_epoch') AS CHAR), '0:batch_id,claim_epoch');

CALL c3_assert('unique.uk_claim_idempotency', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_claims' AND index_name='uk_claim_idempotency') AS CHAR), '0:idempotency_key');

CALL c3_assert('unique.uq_claim_id_batch', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_claims' AND index_name='uq_claim_id_batch') AS CHAR), '0:id,batch_id');

CALL c3_assert('unique.uq_claim_id_batch_epoch', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_claims' AND index_name='uq_claim_id_batch_epoch') AS CHAR), '0:id,batch_id,claim_epoch');

CALL c3_assert('unique.uq_reservation_claim', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='capacity_reservations' AND index_name='uq_reservation_claim') AS CHAR), '0:claim_id');

CALL c3_assert('foreign_key.fk_batches_current_claim', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_batches_current_claim') AS CHAR), 'current_claim_id>id,id>batch_id,claim_epoch>claim_epoch');

CALL c3_assert('foreign_key.fk_batch_audit_claim_batch', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_batch_audit_claim_batch') AS CHAR), 'claim_id>id,batch_id>batch_id');

CALL c3_assert('foreign_key.fk_reservation_claim_batch', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_reservation_claim_batch') AS CHAR), 'claim_id>id,batch_id>batch_id');

CALL c3_assert('foreign_keys.restrict', CAST((SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema=DATABASE() AND (table_name IN ('batch_claims','capacity_reservations') OR constraint_name IN ('fk_batches_current_claim','fk_batch_audit_claim_batch')) AND delete_rule IN ('RESTRICT','NO ACTION') AND update_rule IN ('RESTRICT','NO ACTION')) AS CHAR), '9');

CALL c3_assert('staging.assignment_null_retained', CAST((SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND constraint_name IN ('ck_batches_c1_assignment_null','ck_command_c1_assignment_null','ck_batch_audit_c1_assignment_null') AND enforced='YES') AS CHAR), '3');

CALL c3_assert('staging.claim_null_replaced', CAST((SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND constraint_name IN ('ck_batches_c1_claim_null','ck_batch_audit_c1_claim_null')) AS CHAR), '0');

CALL c3_clone('claim.valid_second_batch', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002'''), 0, '');

CALL c3_clone('claim.duplicate_epoch', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000001''','idempotency_key','''C3-Fresh-Key-000002'''), 1062, 'uk_batch_claim_epoch');

CALL c3_clone('claim.duplicate_global_key', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Schema-Winner-0001'''), 1062, 'uk_claim_idempotency');

CALL c3_clone('claim.case_only_key', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''c3-schema-winner-0001'''), 1062, 'uk_claim_idempotency');

CALL c3_clone('claim.cross_actor_global_key', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Schema-Winner-0001''','claimed_by','''USR-008''','recycler_org_id','''PROC-002'''), 1062, 'uk_claim_idempotency');

CALL c3_clone('claim.orphan_batch_id', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''missing-parent''','idempotency_key','''C3-Fresh-Key-000002'''), 1452, 'fk_batch_claims_batch');

CALL c3_clone('claim.orphan_recycler_org_id', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','recycler_org_id','''missing-parent'''), 1452, 'fk_batch_claims_org');

CALL c3_clone('claim.orphan_claimed_by', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claimed_by','''missing-parent'''), 1452, 'fk_batch_claims_user');

CALL c3_clone('claim.rejected_claim_epoch_31', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_epoch','0'), 3819, 'ck_claim_epoch');

CALL c3_clone('claim.rejected_claim_status_32', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_status','''accepted'''), 3819, 'chk_claim_status');

CALL c3_clone('claim.rejected_claim_status_33', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_status','''INVALID'''), 3819, 'chk_claim_status');

CALL c3_clone('claim.rejected_idempotency_key_34', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''short'''), 3819, 'ck_claim_key_length');

CALL c3_clone('claim.rejected_created_at_35', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','created_at','''2026-09-17 00:00:00'''), 3819, 'ck_claim_created_time');

CALL c3_clone('claim.rejected_superseded_at_36', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','superseded_at','''2026-09-19 00:00:00'''), 3819, 'ck_claim_superseded_time');

CALL c3_clone('claim.rejected_claim_status_37', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_status','''SUPERSEDED'''), 3819, 'ck_claim_superseded_time');

CALL c3_clone('claim.null_required', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claimed_by','NULL'), 1048, 'claimed_by');

CALL c3_clone('claim.valid_superseded_vocabulary', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_status','''SUPERSEDED''','superseded_at','''2026-09-18 10:00:00'''), 0, '');

CALL c3_clone('claim.valid_rejected_vocabulary', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_status','''REJECTED'''), 0, '');

CALL c3_clone('claim.superseded_before_claim', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','claim_status','''SUPERSEDED''','superseded_at','''2026-09-17 10:00:00'''), 3819, 'ck_claim_superseded_time');

CALL c3_clone('claim.notes_255_unicode', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','''C3-Fresh-Key-000002''','notes','REPEAT(''界'',255)'), 0, '');

CALL c3_clone('claim.key_16', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','REPEAT(''k'',16)'), 0, '');

CALL c3_clone('claim.key_64', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','REPEAT(''k'',64)'), 0, '');

CALL c3_clone('claim.key_65', 'batch_claims', 'f3000000-0000-4000-8000-000000000001', 'f3999999-0000-4000-8000-000000000099', JSON_OBJECT('batch_id','''b3000000-0000-4000-8000-000000000002''','idempotency_key','REPEAT(''k'',65)'), 1406, 'idempotency_key');

CALL c3_clone('reservation.duplicate_claim', 'capacity_reservations', 'a3000000-0000-4000-8000-000000000001', 'a3999999-0000-4000-8000-000000000099', JSON_OBJECT(), 1062, 'uq_reservation_claim');

CALL c3_statement('reservation.cross_batch', 'UPDATE capacity_reservations SET batch_id=''b3000000-0000-4000-8000-000000000002'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 1452, 'fk_reservation_claim_batch');

CALL c3_statement('reservation.orphan_claim', 'UPDATE capacity_reservations SET claim_id=''missing-claim'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 1452, 'fk_reservation_claim_batch');

CALL c3_statement('reservation.orphan_pool', 'UPDATE capacity_reservations SET capacity_pool_id=''missing-pool'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 1452, 'fk_reservation_pool');

CALL c3_statement('reservation.weight_zero', 'UPDATE capacity_reservations SET reserved_kg=0 WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_weight');

CALL c3_statement('reservation.weight_too_large', 'UPDATE capacity_reservations SET reserved_kg=50000.01 WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_weight');

CALL c3_statement('reservation.version_zero', 'UPDATE capacity_reservations SET version=0 WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_version');

CALL c3_statement('reservation.case_status_release_guard', 'UPDATE capacity_reservations SET status=''reserved'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.release_missing_fields', 'UPDATE capacity_reservations SET status=''RELEASED'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.reserved_release_time', 'UPDATE capacity_reservations SET released_at=''2026-09-19 10:00:00'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.reserved_release_reason', 'UPDATE capacity_reservations SET release_reason=''reason'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.reserved_release_command', 'UPDATE capacity_reservations SET release_command_id=''c3000000-0000-4000-8000-000000000001'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.minimum_weight', 'UPDATE capacity_reservations SET reserved_kg=0.10 WHERE id=''a3000000-0000-4000-8000-000000000001''', 0, '');

CALL c3_statement('reservation.maximum_weight', 'UPDATE capacity_reservations SET reserved_kg=50000.00 WHERE id=''a3000000-0000-4000-8000-000000000001''', 0, '');

CALL c3_statement('reservation.valid_release_vocabulary', 'UPDATE capacity_reservations SET status=''RELEASED'',released_at=''2026-09-19 10:00:00'',release_reason=''Fixture release'',release_command_id=''c3000000-0000-4000-8000-000000000001'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 0, '');

CALL c3_statement('reservation.release_blank_reason', 'UPDATE capacity_reservations SET status=''RELEASED'',released_at=''2026-09-19 10:00:00'',release_reason=''   '',release_command_id=''c3000000-0000-4000-8000-000000000001'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.release_before_reservation', 'UPDATE capacity_reservations SET status=''RELEASED'',released_at=''2026-09-17 10:00:00'',release_reason=''Fixture release'',release_command_id=''c3000000-0000-4000-8000-000000000001'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 3819, 'ck_reservation_release');

CALL c3_statement('reservation.release_orphan_command', 'UPDATE capacity_reservations SET status=''RELEASED'',released_at=''2026-09-19 10:00:00'',release_reason=''Fixture release'',release_command_id=''missing-command'' WHERE id=''a3000000-0000-4000-8000-000000000001''', 1452, 'fk_reservation_release_command');

CALL c3_statement('pointer.wrong_batch', 'UPDATE ewaste_batches SET status=''APPROVED'',current_claim_id=''f3000000-0000-4000-8000-000000000001'' WHERE id=''b3000000-0000-4000-8000-000000000002''', 1452, 'fk_batches_current_claim');

CALL c3_statement('pointer.wrong_epoch', 'UPDATE ewaste_batches SET claim_epoch=2 WHERE id=''b3000000-0000-4000-8000-000000000001''', 1452, 'fk_batches_current_claim');

CALL c3_statement('pointer.approved_requires_claim', 'UPDATE ewaste_batches SET current_claim_id=NULL WHERE id=''b3000000-0000-4000-8000-000000000001''', 3819, 'ck_batches_claim_state');

CALL c3_statement('pointer.matched_forbids_claim', 'UPDATE ewaste_batches SET status=''MATCHED'' WHERE id=''b3000000-0000-4000-8000-000000000001''', 3819, 'ck_batches_claim_state');

CALL c3_statement('audit.wrong_batch', 'UPDATE batch_audit_events SET batch_id=''b3000000-0000-4000-8000-000000000002'' WHERE id=''e3000000-0000-4000-8000-000000000001''', 1452, 'fk_batch_audit_claim_batch');

CALL c3_statement('batch.assignment_still_null', 'UPDATE ewaste_batches SET current_assignment_id=''11111111-1111-4111-8111-111111111111'' WHERE id=''b3000000-0000-4000-8000-000000000001''', 3819, 'ck_batches_c1_assignment_null');

CALL c3_statement('audit.assignment_still_null', 'UPDATE batch_audit_events SET assignment_id=''11111111-1111-4111-8111-111111111111'' WHERE id=''e3000000-0000-4000-8000-000000000001''', 3819, 'ck_batch_audit_c1_assignment_null');

CALL c3_statement('claim.history_restrict', 'DELETE FROM batch_claims WHERE id=''f3000000-0000-4000-8000-000000000001''', 1451, '');

CALL c3_statement('pool.history_restrict', 'DELETE FROM recycler_capacity_pools WHERE id=''d3000000-0000-4000-8000-000000000001''', 1451, 'fk_reservation_pool');

CALL c3_clone('command.exact_scope_duplicate', 'command_idempotency', 'c3000000-0000-4000-8000-000000000001', 'c3999999-0000-4000-8000-000000000099', JSON_OBJECT(), 1062, 'uq_command_replay');

CALL c3_clone('command.case_sensitive_key', 'command_idempotency', 'c3000000-0000-4000-8000-000000000001', 'c3999999-0000-4000-8000-000000000099', JSON_OBJECT('idempotency_key','''c3-schema-winner-0001'''), 0, '');

CALL c3_clone('command.separate_actor_scope', 'command_idempotency', 'c3000000-0000-4000-8000-000000000001', 'c3999999-0000-4000-8000-000000000099', JSON_OBJECT('actor_user_id','''USR-008''','actor_scope','''user:USR-008'''), 0, '');

CALL c3_assert('schema.reservation_checks', CAST((SELECT GROUP_CONCAT(constraint_name ORDER BY constraint_name) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND table_name='capacity_reservations' AND constraint_type='CHECK' AND enforced='YES') AS CHAR), 'ck_reservation_release,ck_reservation_status,ck_reservation_version,ck_reservation_weight');

CALL c3_assert('schema.claim_checks', CAST((SELECT GROUP_CONCAT(constraint_name ORDER BY constraint_name) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND table_name='batch_claims' AND constraint_type='CHECK' AND enforced='YES') AS CHAR), 'chk_claim_status,ck_claim_created_time,ck_claim_epoch,ck_claim_key_length,ck_claim_superseded_time');

SELECT test_name,result,actual,expected FROM c3_results ORDER BY sequence_id;

CALL c3_finish();

DROP PROCEDURE c3_clone;

DROP PROCEDURE c3_statement;

DROP PROCEDURE c3_assert;

DROP PROCEDURE c3_finish;

DROP TEMPORARY TABLE c3_results;
PERSISTENCE_EMBED_008

  mkdir -p "$WORK_DIR/database/tests/c4"
  # Embedded database/tests/c4/schema-fixtures.sql
  cat > "$WORK_DIR/database/tests/c4/schema-fixtures.sql" <<'PERSISTENCE_EMBED_009'
-- Deterministic row-constraint fixtures, run once on the runner's disposable DB.
-- Requires @seed plus tests/c3/schema-fixtures.sql and C4 migrations.
-- These fixtures exercise database constraints, not application commands.
SET NAMES utf8mb4;
SET time_zone = '+00:00';
START TRANSACTION;
INSERT INTO recycler_collector_scopes
    (id,recycler_org_id,collector_org_id,zone,is_active,version,valid_from,valid_until,created_at,updated_at)
VALUES
    ('54000000-0000-4000-8000-000000000001','PROC-001','COL-001','CENTRAL',1,1,'2026-09-01',NULL,'2026-09-01','2026-09-01'),
    ('54000000-0000-4000-8000-000000000002','PROC-001','COL-002','CENTRAL',1,1,'2026-09-01',NULL,'2026-09-01','2026-09-01');

INSERT INTO batch_claims
    (id,batch_id,claim_epoch,recycler_org_id,claimed_by,claim_status,idempotency_key,claimed_at,created_at)
VALUES ('f3000000-0000-4000-8000-000000000002','b3000000-0000-4000-8000-000000000002',1,
    'PROC-001','USR-007','ACCEPTED','C4-Schema-Claim-0002','2026-09-18 10:00:00','2026-09-18 10:00:00');
INSERT INTO capacity_reservations
    (id,batch_id,claim_id,capacity_pool_id,reserved_kg,status,reserved_at,version)
VALUES ('a3000000-0000-4000-8000-000000000002','b3000000-0000-4000-8000-000000000002',
    'f3000000-0000-4000-8000-000000000002','d3000000-0000-4000-8000-000000000001',100,'RESERVED','2026-09-18 10:00:00',1);
UPDATE recycler_capacity_pools SET reserved_kg=200,version=3,updated_at='2026-09-18 10:00:00'
WHERE id='d3000000-0000-4000-8000-000000000001';
UPDATE ewaste_batches SET status='APPROVED',version=4,current_claim_id='f3000000-0000-4000-8000-000000000002',updated_at='2026-09-18 10:00:00'
WHERE id='b3000000-0000-4000-8000-000000000002';

INSERT INTO batch_assignments
    (id,batch_id,claim_id,recycler_org_id,collector_org_id,collector_user_id,collector_scope_id,
     assignment_sequence,claim_epoch,assignment_status,assigned_at,responded_at,closed_at,closure_reason,
     version,created_at,updated_at)
VALUES
    ('a4000000-0000-4000-8000-000000000001','b3000000-0000-4000-8000-000000000001',
     'f3000000-0000-4000-8000-000000000001','PROC-001','COL-001','USR-005',
     '54000000-0000-4000-8000-000000000001',1,1,'COMPLETED',
     '2026-09-19 09:00:00','2026-09-19 09:00:00','2026-09-19 10:00:00','COLLECTED',2,
     '2026-09-19 09:00:00','2026-09-19 10:00:00'),
    ('a4000000-0000-4000-8000-000000000002','b3000000-0000-4000-8000-000000000002',
     'f3000000-0000-4000-8000-000000000002','PROC-001','COL-001','USR-005',
     '54000000-0000-4000-8000-000000000001',1,1,'ACCEPTED',
     '2026-09-19 09:00:00','2026-09-19 09:00:00',NULL,NULL,1,
     '2026-09-19 09:00:00','2026-09-19 09:00:00');
UPDATE ewaste_batches SET status='COLLECTED',version=6,current_assignment_id='a4000000-0000-4000-8000-000000000001',updated_at='2026-09-19 10:00:00'
WHERE id='b3000000-0000-4000-8000-000000000001';
UPDATE ewaste_batches SET status='ASSIGNED',version=5,current_assignment_id='a4000000-0000-4000-8000-000000000002',updated_at='2026-09-19 09:00:00'
WHERE id='b3000000-0000-4000-8000-000000000002';

INSERT INTO command_idempotency
    (id,actor_user_id,actor_scope,command_name,idempotency_key,request_hash,batch_id,assignment_id,
     state,response_status,response_json,created_at,completed_at,retain_until)
VALUES ('c4000000-0000-4000-8000-000000000001','USR-005','user:USR-005','RecordCollectionHandoff',
    'C4-Schema-Handoff-0001',SHA2('C4 row constraints',256),'b3000000-0000-4000-8000-000000000001',
    'a4000000-0000-4000-8000-000000000001','COMPLETED',200,JSON_OBJECT('batch_status','COLLECTED'),
    '2026-09-19 10:00:00','2026-09-19 10:00:00','2027-09-19 10:00:00');

INSERT INTO batch_handoffs
    (id,batch_id,assignment_id,collector_user_id,collector_org_id,pickup_status,donor_representative_name,
     actual_item_count,verification_hash,pickup_occurred_at,recorded_at,collected_at,command_id,correlation_id,created_at)
VALUES ('64000000-0000-4000-8000-000000000001','b3000000-0000-4000-8000-000000000001',
    'a4000000-0000-4000-8000-000000000001','USR-005','COL-001','COLLECTED','Synthetic Representative',
    5,REPEAT('a',64),'2026-09-19 10:00:00','2026-09-19 10:00:00','2026-09-19 10:00:00',
    'c4000000-0000-4000-8000-000000000001','c4-schema-fixture','2026-09-19 10:00:00');

INSERT INTO assignment_actions
    (id,batch_id,assignment_id,action_type,actor_user_id,from_status,to_status,assignment_version,
     command_id,occurred_at,correlation_id,details_json)
VALUES ('d4000000-0000-4000-8000-000000000001','b3000000-0000-4000-8000-000000000001',
    'a4000000-0000-4000-8000-000000000001','HANDOFF_RECORDED','USR-005','ASSIGNED','COLLECTED',2,
    'c4000000-0000-4000-8000-000000000001','2026-09-19 10:00:00','c4-schema-fixture',
    JSON_OBJECT('handoff_id','64000000-0000-4000-8000-000000000001','scope_version','1'));
INSERT INTO batch_audit_events
    (id,batch_id,command_id,assignment_id,claim_id,actor_user_id,actor_org_id,event_type,from_status,to_status,
     batch_version,sequence_in_command,occurred_at,correlation_id,details_json)
VALUES ('e4000000-0000-4000-8000-000000000001','b3000000-0000-4000-8000-000000000001',
    'c4000000-0000-4000-8000-000000000001','a4000000-0000-4000-8000-000000000001',
    'f3000000-0000-4000-8000-000000000001','USR-005','COL-001','CollectionCompleted','ASSIGNED','COLLECTED',
    6,1,'2026-09-19 10:00:00','c4-schema-fixture',JSON_OBJECT('fixture','row-constraints'));
COMMIT;
PERSISTENCE_EMBED_009

  mkdir -p "$WORK_DIR/database/tests/c4"
  # Embedded database/tests/c4/verify-schema.sql
  cat > "$WORK_DIR/database/tests/c4/verify-schema.sql" <<'PERSISTENCE_EMBED_010'
-- C4 runtime checks. Run only in the runner's disposable MySQL database.
-- All mutation checks roll back; load schema-fixtures.sql first.
-- Expected errors include the exact MySQL error number and named constraint.
SET NAMES utf8mb4;
SET time_zone = '+00:00';
SET SESSION group_concat_max_len = 16384;
CREATE TEMPORARY TABLE c4_results (
    sequence_id INT AUTO_INCREMENT PRIMARY KEY,
    test_name VARCHAR(128) NOT NULL UNIQUE,
    result VARCHAR(4) NOT NULL,
    actual VARCHAR(4096),
    expected VARCHAR(4096)
) ENGINE=MEMORY;
DELIMITER $$
CREATE PROCEDURE c4_assert(IN p_name VARCHAR(128), IN p_actual TEXT, IN p_expected TEXT)
BEGIN
    INSERT INTO c4_results(test_name, result, actual, expected)
    VALUES(p_name, IF(BINARY p_actual <=> BINARY p_expected, 'PASS', 'FAIL'), p_actual, p_expected);
END$$
CREATE PROCEDURE c4_statement(
    IN p_name VARCHAR(128), IN p_sql LONGTEXT,
    IN p_errno INT, IN p_constraint VARCHAR(128))
BEGIN
    DECLARE observed INT DEFAULT 0;
    DECLARE message_text_value TEXT DEFAULT '';
    DECLARE affected INT DEFAULT 0;
    DECLARE prepared_ok BOOLEAN DEFAULT FALSE;
    START TRANSACTION;
    BEGIN
        DECLARE EXIT HANDLER FOR SQLEXCEPTION
            GET DIAGNOSTICS CONDITION 1 observed = MYSQL_ERRNO, message_text_value = MESSAGE_TEXT;
        SET @c4_statement = p_sql;
        PREPARE c4_prepared FROM @c4_statement;
        SET prepared_ok = TRUE;
        EXECUTE c4_prepared;
        SET affected = ROW_COUNT();
    END;
    IF prepared_ok THEN DEALLOCATE PREPARE c4_prepared; END IF;
    ROLLBACK;
    INSERT INTO c4_results(test_name, result, actual, expected)
    VALUES(p_name,
        IF(observed = p_errno
           AND (p_errno <> 0 OR affected = 1)
           AND (p_constraint = '' OR LOCATE(p_constraint, message_text_value) > 0), 'PASS', 'FAIL'),
        IF(observed = 0, CONCAT('accepted rows=', affected), CONCAT(observed, ': ', message_text_value)),
        IF(p_errno = 0, 'accepted rows=1', CONCAT(p_errno, ': ', p_constraint)));
END$$
CREATE PROCEDURE c4_clone(
    IN p_name VARCHAR(128), IN p_table VARCHAR(64), IN p_source VARCHAR(36),
    IN p_id VARCHAR(36), IN p_patch JSON, IN p_errno INT, IN p_constraint VARCHAR(128))
BEGIN
    DECLARE columns_sql TEXT;
    DECLARE values_sql TEXT;
    DECLARE pk_name VARCHAR(16);
    IF p_table NOT IN ('recycler_collector_scopes','batch_assignments','batch_handoffs','assignment_actions','ewaste_batches','command_idempotency','batch_audit_events') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Invalid test table';
    END IF;
    SET pk_name = IF(p_table = 'event_outbox', 'event_id', 'id');
    SELECT GROUP_CONCAT(CONCAT('`', COLUMN_NAME, '`') ORDER BY ORDINAL_POSITION),
           GROUP_CONCAT(CASE
               WHEN JSON_CONTAINS_PATH(p_patch, 'one', CONCAT('$.', COLUMN_NAME))
                   THEN JSON_UNQUOTE(JSON_EXTRACT(p_patch, CONCAT('$.', COLUMN_NAME)))
               WHEN COLUMN_NAME = pk_name THEN QUOTE(p_id)
               ELSE CONCAT('b.`', COLUMN_NAME, '`') END ORDER BY ORDINAL_POSITION)
    INTO columns_sql, values_sql
    FROM information_schema.columns WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = p_table AND GENERATION_EXPRESSION = '';
    CALL c4_statement(p_name,
        CONCAT('INSERT INTO ', p_table, ' (', columns_sql, ') SELECT ', values_sql,
               ' FROM ', p_table, ' b WHERE b.', pk_name, ' = ', QUOTE(p_source)),
        p_errno, p_constraint);
END$$
CREATE PROCEDURE c4_finish()
BEGIN
    IF EXISTS(SELECT 1 FROM c4_results WHERE result <> 'PASS') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'C4 persistence check failed';
    END IF;
END$$
DELIMITER ;





CALL c4_assert('schema.recycler_collector_scopes.columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='recycler_collector_scopes') AS CHAR), '10');

CALL c4_assert('schema.recycler_collector_scopes.engine', CAST((SELECT CONCAT(engine,'/',table_collation) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='recycler_collector_scopes') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c4_assert('schema.batch_assignments.columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_assignments') AS CHAR), '21');

CALL c4_assert('schema.batch_assignments.engine', CAST((SELECT CONCAT(engine,'/',table_collation) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_assignments') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c4_assert('schema.batch_handoffs.columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_handoffs') AS CHAR), '18');

CALL c4_assert('schema.batch_handoffs.engine', CAST((SELECT CONCAT(engine,'/',table_collation) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='batch_handoffs') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c4_assert('schema.assignment_actions.columns', CAST((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='assignment_actions') AS CHAR), '15');

CALL c4_assert('schema.assignment_actions.engine', CAST((SELECT CONCAT(engine,'/',table_collation) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='assignment_actions') AS CHAR), 'InnoDB/utf8mb4_0900_ai_ci');

CALL c4_assert('schema.generated_active_slot', CAST((SELECT CONCAT(extra,'/',is_nullable) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='batch_assignments' AND column_name='active_batch_id') AS CHAR), 'STORED GENERATED/YES');

CALL c4_assert('fixture.closed_slot_null', CAST((SELECT active_batch_id IS NULL FROM batch_assignments WHERE id='a4000000-0000-4000-8000-000000000001') AS CHAR), '1');

CALL c4_assert('fixture.open_slot_batch', CAST((SELECT active_batch_id FROM batch_assignments WHERE id='a4000000-0000-4000-8000-000000000002') AS CHAR), 'b3000000-0000-4000-8000-000000000002');

CALL c4_assert('schema.c3_claim_guards_retained', CAST((SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND constraint_name IN ('fk_batches_current_claim','ck_batches_claim_state','fk_batch_audit_claim_batch')) AS CHAR), '3');

CALL c4_assert('schema.assignment_staging_replaced', CAST((SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND constraint_name IN ('ck_batches_c1_assignment_null','ck_command_c1_assignment_null','ck_batch_audit_c1_assignment_null')) AS CHAR), '0');

CALL c4_assert('unique.uq_assignment_open', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_assignments' AND index_name='uq_assignment_open') AS CHAR), '0:active_batch_id');

CALL c4_assert('unique.uq_assignment_sequence', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_assignments' AND index_name='uq_assignment_sequence') AS CHAR), '0:batch_id,assignment_sequence');

CALL c4_assert('unique.uq_assignment_current_tuple', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_assignments' AND index_name='uq_assignment_current_tuple') AS CHAR), '0:id,batch_id,claim_id,claim_epoch');

CALL c4_assert('unique.uq_assignment_actor_tuple', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_assignments' AND index_name='uq_assignment_actor_tuple') AS CHAR), '0:id,batch_id,collector_user_id,collector_org_id');

CALL c4_assert('unique.uq_handoff_assignment', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_handoffs' AND index_name='uq_handoff_assignment') AS CHAR), '0:assignment_id');

CALL c4_assert('unique.uq_handoff_command', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_handoffs' AND index_name='uq_handoff_command') AS CHAR), '0:command_id');

CALL c4_assert('unique.uq_assignment_action_command', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='assignment_actions' AND index_name='uq_assignment_action_command') AS CHAR), '0:command_id,assignment_id,action_type');

CALL c4_assert('unique.uq_collector_scope_pair_zone', CAST((SELECT CONCAT(MIN(non_unique),':',GROUP_CONCAT(column_name ORDER BY seq_in_index)) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='recycler_collector_scopes' AND index_name='uq_collector_scope_pair_zone') AS CHAR), '0:recycler_org_id,collector_org_id,zone');

CALL c4_assert('fk.fk_batches_current_assignment', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_batches_current_assignment') AS CHAR), 'current_assignment_id>id,id>batch_id,current_claim_id>claim_id,claim_epoch>claim_epoch');

CALL c4_assert('fk.fk_assignment_claim_epoch', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_assignment_claim_epoch') AS CHAR), 'claim_id>id,batch_id>batch_id,claim_epoch>claim_epoch');

CALL c4_assert('fk.fk_assignment_scope_pair', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_assignment_scope_pair') AS CHAR), 'collector_scope_id>id,recycler_org_id>recycler_org_id,collector_org_id>collector_org_id');

CALL c4_assert('fk.fk_handoff_assignment_actor', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_handoff_assignment_actor') AS CHAR), 'assignment_id>id,batch_id>batch_id,collector_user_id>collector_user_id,collector_org_id>collector_org_id');

CALL c4_assert('fk.fk_command_assignment_batch', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_command_assignment_batch') AS CHAR), 'assignment_id>id,batch_id>batch_id');

CALL c4_assert('fk.fk_batch_audit_assignment_batch', CAST((SELECT GROUP_CONCAT(CONCAT(column_name,'>',referenced_column_name) ORDER BY ordinal_position) FROM information_schema.key_column_usage WHERE constraint_schema=DATABASE() AND constraint_name='fk_batch_audit_assignment_batch') AS CHAR), 'assignment_id>id,batch_id>batch_id');

CALL c4_assert('schema.restrictive_c4_foreign_keys', CAST((SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema=DATABASE() AND (table_name IN ('recycler_collector_scopes','batch_assignments','batch_handoffs','assignment_actions') OR constraint_name IN ('fk_batches_current_assignment','fk_command_assignment_batch','fk_batch_audit_assignment_batch')) AND delete_rule IN ('RESTRICT','NO ACTION') AND update_rule IN ('RESTRICT','NO ACTION')) AS CHAR), '18');

CALL c4_statement('scope.invalid_zone', 'UPDATE recycler_collector_scopes SET zone=''NORTHEAST'' WHERE id=''54000000-0000-4000-8000-000000000001''', 3819, 'ck_collector_scope_zone');

CALL c4_statement('scope.invalid_active', 'UPDATE recycler_collector_scopes SET is_active=2 WHERE id=''54000000-0000-4000-8000-000000000001''', 3819, 'ck_collector_scope_active');

CALL c4_statement('scope.zero_version', 'UPDATE recycler_collector_scopes SET version=0 WHERE id=''54000000-0000-4000-8000-000000000001''', 3819, 'ck_collector_scope_version');

CALL c4_statement('scope.empty_interval', 'UPDATE recycler_collector_scopes SET valid_until=valid_from WHERE id=''54000000-0000-4000-8000-000000000001''', 3819, 'ck_collector_scope_interval');

CALL c4_statement('scope.time_reversal', 'UPDATE recycler_collector_scopes SET updated_at=created_at-INTERVAL 1 SECOND WHERE id=''54000000-0000-4000-8000-000000000001''', 3819, 'ck_collector_scope_updated');

CALL c4_statement('scope.valid_future_interval', 'UPDATE recycler_collector_scopes SET valid_until=''2026-12-01'' WHERE id=''54000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_clone('scope.unique_pair_zone','recycler_collector_scopes','54000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT(),1062,'uq_collector_scope_pair_zone');

CALL c4_clone('scope.orphan_recycler','recycler_collector_scopes','54000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('recycler_org_id','''missing-recycler'''),1452,'fk_collector_scope_recycler');

CALL c4_clone('scope.orphan_collector','recycler_collector_scopes','54000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('collector_org_id','''missing-collector'''),1452,'fk_collector_scope_collector');

CALL c4_clone('assignment.closed_history_successor','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor'''),0,'');

CALL c4_clone('assignment.duplicate_sequence','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT(),1062,'uq_assignment_sequence');

CALL c4_clone('assignment.one_open_slot','batch_assignments','a4000000-0000-4000-8000-000000000002','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000002''','reassignment_reason','''Another choice'''),1062,'uq_assignment_open');

CALL c4_clone('assignment.orphan_collector','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','collector_user_id','''missing-user'''),1452,'fk_assignment_collector');

CALL c4_clone('assignment.cross_batch_claim','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','claim_id','''f3000000-0000-4000-8000-000000000002'''),1452,'fk_assignment_claim_epoch');

CALL c4_clone('assignment.wrong_claim_epoch','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','claim_epoch','2'),1452,'fk_assignment_claim_epoch');

CALL c4_clone('assignment.wrong_scope_pair','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','collector_org_id','''COL-002'''),1452,'fk_assignment_scope_pair');

CALL c4_clone('assignment.wrong_predecessor_batch','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000002''','reassignment_reason','''Schema successor'''),1452,'fk_assignment_previous_batch');

CALL c4_clone('assignment.self_predecessor','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''94000000-0000-4000-8000-000000000099''','reassignment_reason','''Schema successor'''),3819,'ck_assignment_predecessor');

CALL c4_clone('assignment.no_reassignment_reason','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','NULL'),3819,'ck_assignment_predecessor');

CALL c4_clone('assignment.blank_reassignment_reason','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''   '''),3819,'ck_assignment_predecessor');

CALL c4_clone('assignment.zero_version','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','version','0'),3819,'ck_assignment_version');

CALL c4_clone('assignment.terminal_requires_close','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','closed_at','NULL'),3819,'ck_assignment_closure');

CALL c4_clone('assignment.terminal_requires_reason','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','closure_reason','NULL'),3819,'ck_assignment_closure');

CALL c4_clone('assignment.blank_closure_reason','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','closure_reason',''' '''),3819,'ck_assignment_closure');

CALL c4_clone('assignment.closed_before_assigned','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','closed_at','''2026-09-19 08:00:00''','responded_at','NULL'),3819,'ck_assignment_closure');

CALL c4_clone('assignment.responded_after_closed','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','responded_at','''2026-09-19 10:00:00.000001''','updated_at','''2026-09-19 11:00:00'''),3819,'ck_assignment_response_before_close');

CALL c4_clone('assignment.blank_rejection_reason','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','rejection_reason',''' '''),3819,'ck_assignment_rejection_reason');

CALL c4_clone('assignment.legacy_null_acceptance','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','responded_at','NULL'),0,'');

CALL c4_clone('assignment.accepted_requires_response','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','assignment_status','''ACCEPTED''','responded_at','NULL','closed_at','NULL','closure_reason','NULL'),3819,'ck_assignment_response');

CALL c4_clone('assignment.pending_forbids_response','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','assignment_status','''PENDING''','closed_at','NULL','closure_reason','NULL'),3819,'ck_assignment_response');

CALL c4_clone('assignment.valid_pending','batch_assignments','a4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_sequence','2','previous_assignment_id','''a4000000-0000-4000-8000-000000000001''','reassignment_reason','''Schema successor''','assignment_status','''PENDING''','responded_at','NULL','closed_at','NULL','closure_reason','NULL'),0,'');

CALL c4_statement('assignment.generated_not_input', 'UPDATE batch_assignments SET active_batch_id=''b3000000-0000-4000-8000-000000000001'' WHERE id=''a4000000-0000-4000-8000-000000000001''', 3105, 'active_batch_id');

CALL c4_statement('handoff.success_requires_name', 'UPDATE batch_handoffs SET donor_representative_name=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.blank_name', 'UPDATE batch_handoffs SET donor_representative_name='' '' WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_donor_name');

CALL c4_statement('handoff.success_requires_count', 'UPDATE batch_handoffs SET actual_item_count=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.count_zero', 'UPDATE batch_handoffs SET actual_item_count=0 WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_count');

CALL c4_statement('handoff.count_overflow', 'UPDATE batch_handoffs SET actual_item_count=100001 WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_count');

CALL c4_statement('handoff.count_max', 'UPDATE batch_handoffs SET actual_item_count=100000 WHERE id=''64000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_statement('handoff.success_requires_hash', 'UPDATE batch_handoffs SET verification_hash=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.hash_short', 'UPDATE batch_handoffs SET verification_hash=REPEAT(''a'',63) WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_hash');

CALL c4_statement('handoff.hash_nonhex', 'UPDATE batch_handoffs SET verification_hash=REPEAT(''z'',64) WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_hash');

CALL c4_statement('handoff.hash_uppercase', 'UPDATE batch_handoffs SET verification_hash=REPEAT(''A'',64) WHERE id=''64000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_statement('handoff.success_requires_collected', 'UPDATE batch_handoffs SET collected_at=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.collected_equals_pickup', 'UPDATE batch_handoffs SET collected_at=pickup_occurred_at-INTERVAL 1 MICROSECOND WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.future_pickup', 'UPDATE batch_handoffs SET pickup_occurred_at=recorded_at+INTERVAL 1 MICROSECOND,collected_at=recorded_at+INTERVAL 1 MICROSECOND WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_times');

CALL c4_statement('handoff.recorded_after_created', 'UPDATE batch_handoffs SET recorded_at=created_at+INTERVAL 1 MICROSECOND WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_times');

CALL c4_statement('handoff.failure_requires_reason', 'UPDATE batch_handoffs SET pickup_status=''FAILED_COLLECTION'',collected_at=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.failure_invalid_reason', 'UPDATE batch_handoffs SET pickup_status=''FAILED_COLLECTION'',collected_at=NULL,failure_reason=''OTHER'' WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_outcome');

CALL c4_statement('handoff.failure_without_fabricated_evidence', 'UPDATE batch_handoffs SET pickup_status=''FAILED_COLLECTION'',collected_at=NULL,failure_reason=''DONOR_UNAVAILABLE'',donor_representative_name=NULL,actual_item_count=NULL,verification_hash=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_statement('handoff.failure_observed_count_optional', 'UPDATE batch_handoffs SET pickup_status=''FAILED_COLLECTION'',collected_at=NULL,failure_reason=''INCORRECT_ITEMS'',actual_item_count=7 WHERE id=''64000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_statement('handoff.optional_discrepancy', 'UPDATE batch_handoffs SET actual_item_count=7,quantity_discrepancy_reason=NULL WHERE id=''64000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_statement('handoff.blank_discrepancy', 'UPDATE batch_handoffs SET quantity_discrepancy_reason='' '' WHERE id=''64000000-0000-4000-8000-000000000001''', 3819, 'ck_handoff_discrepancy');

CALL c4_statement('handoff.wrong_collector', 'UPDATE batch_handoffs SET collector_user_id=''USR-006'' WHERE id=''64000000-0000-4000-8000-000000000001''', 1452, 'fk_handoff_assignment_actor');

CALL c4_statement('handoff.wrong_collector_org', 'UPDATE batch_handoffs SET collector_org_id=''COL-002'' WHERE id=''64000000-0000-4000-8000-000000000001''', 1452, 'fk_handoff_assignment_actor');

CALL c4_statement('handoff.wrong_batch', 'UPDATE batch_handoffs SET batch_id=''b3000000-0000-4000-8000-000000000002'' WHERE id=''64000000-0000-4000-8000-000000000001''', 1452, 'fk_handoff_assignment_actor');

CALL c4_statement('handoff.orphan_command', 'UPDATE batch_handoffs SET command_id=''missing-command'' WHERE id=''64000000-0000-4000-8000-000000000001''', 1452, 'fk_handoff_command');

CALL c4_clone('handoff.one_outcome_per_assignment','batch_handoffs','64000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('command_id','''c3000000-0000-4000-8000-000000000001'''),1062,'uq_handoff_assignment');

CALL c4_clone('handoff.one_outcome_per_command','batch_handoffs','64000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT('assignment_id','''a4000000-0000-4000-8000-000000000002''','batch_id','''b3000000-0000-4000-8000-000000000002'''),1062,'uq_handoff_command');

CALL c4_clone('action.unique_command_type','assignment_actions','d4000000-0000-4000-8000-000000000001','94000000-0000-4000-8000-000000000099',JSON_OBJECT(),1062,'uq_assignment_action_command');

CALL c4_statement('action.wrong_type', 'UPDATE assignment_actions SET action_type=''RECOVERED'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 3819, 'ck_assignment_action_type');

CALL c4_statement('action.no_actor', 'UPDATE assignment_actions SET actor_user_id=NULL WHERE id=''d4000000-0000-4000-8000-000000000001''', 3819, 'ck_assignment_action_actor');

CALL c4_statement('action.two_actors', 'UPDATE assignment_actions SET service_principal=''recovery-service'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 3819, 'ck_assignment_action_actor');

CALL c4_statement('action.service_actor', 'UPDATE assignment_actions SET actor_user_id=NULL,service_principal=''fixture-service'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 0, '');

CALL c4_statement('action.rejection_needs_reason', 'UPDATE assignment_actions SET action_type=''REJECTED'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 3819, 'ck_assignment_action_reason');

CALL c4_statement('action.reassignment_needs_previous', 'UPDATE assignment_actions SET action_type=''REASSIGNED'',reason=''fixture reason'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 3819, 'ck_assignment_action_previous');

CALL c4_statement('action.wrong_predecessor_batch', 'UPDATE assignment_actions SET previous_assignment_id=''a4000000-0000-4000-8000-000000000002'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 1452, 'fk_assignment_action_previous');

CALL c4_statement('action.wrong_assignment_batch', 'UPDATE assignment_actions SET assignment_id=''a4000000-0000-4000-8000-000000000002'' WHERE id=''d4000000-0000-4000-8000-000000000001''', 1452, 'fk_assignment_action_attempt');

CALL c4_statement('action.version_zero', 'UPDATE assignment_actions SET assignment_version=0 WHERE id=''d4000000-0000-4000-8000-000000000001''', 3819, 'ck_assignment_action_version');

CALL c4_statement('pointer.cross_batch', 'UPDATE ewaste_batches SET current_assignment_id=''a4000000-0000-4000-8000-000000000002'' WHERE id=''b3000000-0000-4000-8000-000000000001''', 1452, 'fk_batches_current_assignment');

CALL c4_statement('pointer.custody_requires_assignment', 'UPDATE ewaste_batches SET current_assignment_id=NULL WHERE id=''b3000000-0000-4000-8000-000000000001''', 3819, 'ck_batches_assignment_state');

CALL c4_statement('pointer.approved_forbids_assignment', 'UPDATE ewaste_batches SET status=''APPROVED'' WHERE id=''b3000000-0000-4000-8000-000000000001''', 3819, 'ck_batches_assignment_state');

CALL c4_statement('command.cross_batch', 'UPDATE command_idempotency SET batch_id=''b3000000-0000-4000-8000-000000000002'' WHERE id=''c4000000-0000-4000-8000-000000000001''', 1452, 'fk_command_assignment_batch');

CALL c4_statement('command.null_pair_bypass', 'UPDATE command_idempotency SET batch_id=NULL WHERE id=''c4000000-0000-4000-8000-000000000001''', 3819, 'ck_command_assignment_batch');

CALL c4_statement('audit.cross_batch', 'UPDATE batch_audit_events SET assignment_id=''a4000000-0000-4000-8000-000000000002'' WHERE id=''e4000000-0000-4000-8000-000000000001''', 1452, 'fk_batch_audit_assignment_batch');

CALL c4_statement('history.restrict_assignment', 'DELETE FROM batch_assignments WHERE id=''a4000000-0000-4000-8000-000000000001''', 1451, '');

CALL c4_statement('history.restrict_scope', 'DELETE FROM recycler_collector_scopes WHERE id=''54000000-0000-4000-8000-000000000001''', 1451, 'fk_assignment_scope_pair');

SELECT test_name,result,actual,expected FROM c4_results ORDER BY sequence_id;

CALL c4_finish();

DROP PROCEDURE c4_clone;

DROP PROCEDURE c4_statement;

DROP PROCEDURE c4_assert;

DROP PROCEDURE c4_finish;

DROP TEMPORARY TABLE c4_results;
PERSISTENCE_EMBED_010

  mkdir -p "$WORK_DIR/database/seed"
  # Embedded database/seed/104-seed-c1-batches.sql
  cat > "$WORK_DIR/database/seed/104-seed-c1-batches.sql" <<'PERSISTENCE_EMBED_011'
--liquibase formatted sql

--changeset team5:EWCSB126-104 dbms:mysql context:@c1-fixtures labels:synthetic-test-data
--preconditions onFail:HALT onError:HALT
--precondition-sql-check expectedResult:2 SELECT COUNT(*) FROM users WHERE (user_id = 'USR-003' AND organisation_id = 'DON-001' AND role_code = 'DONOR' AND status = 'ACTIVE') OR (user_id = 'USR-004' AND organisation_id = 'DON-002' AND role_code = 'DONOR' AND status = 'ACTIVE');
--comment: Explicit opt-in C1 fixtures. Use context-filter=seed,c1-fixtures on isolated test databases.
-- Fixed UUIDv4 values and UTC dates; no NOW(), random IDs, UPSERT or history deletion.
-- Liquibase runs this transaction once; a second update is a no-op.
SET time_zone = '+00:00';

INSERT INTO ewaste_batches (
    id, organization_id, created_by, status, category, quantity,
    estimated_weight_kg, condition_rating, is_data_bearing, zone,
    collection_deadline, notes, claim_epoch, current_claim_id,
    current_assignment_id, version, submitted_at, created_at, updated_at
) VALUES
('b1260000-0000-4000-8000-000000000001', 'DON-001', 'USR-003', 'DRAFT',
 NULL, NULL, NULL, NULL, FALSE, NULL, NULL, NULL,
 1, NULL, NULL, 1, NULL, '2026-09-01 00:00:00.000000', '2026-09-01 00:00:00.000000'),
('b1260000-0000-4000-8000-000000000002', 'DON-002', 'USR-004', 'DRAFT',
 'BATTERIES', 1, NULL, NULL, TRUE, 'WEST', NULL, 'Partial synthetic draft',
 1, NULL, NULL, 1, NULL, '2026-09-01 00:00:00.000000', '2026-09-01 00:00:00.000000'),
('b1260000-0000-4000-8000-000000000003', 'DON-001', 'USR-003', 'SUBMITTED',
 'ICT_EQUIPMENT', 1, 0.10, 'FUNCTIONAL', FALSE, 'NORTH', '2026-09-03 01:00:00.000000', NULL,
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000004', 'DON-002', 'USR-004', 'SUBMITTED',
 'LARGE_APPLIANCE', 100000, 50000.00, 'END_OF_LIFE', TRUE, 'SOUTH', '2026-11-30 01:00:00.000000', REPEAT('界', 500),
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000005', 'DON-001', 'USR-003', 'SUBMITTED',
 'BATTERIES', 10, 12.34, 'REPAIRABLE', TRUE, 'EAST', '2026-09-04 01:00:00.000000', '',
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000006', 'DON-002', 'USR-004', 'SUBMITTED',
 'CONSUMER_ELECTRONICS', 25, 250.50, 'FUNCTIONAL', FALSE, 'WEST', '2026-09-05 01:00:00.000000', 'Synthetic electronics',
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000'),
('b1260000-0000-4000-8000-000000000007', 'DON-001', 'USR-003', 'SUBMITTED',
 'ICT_EQUIPMENT', 5, 100.00, 'REPAIRABLE', TRUE, 'CENTRAL', '2026-09-08 01:00:00.000000', 'Synthetic ICT',
 1, NULL, NULL, 2, '2026-09-01 01:00:00.000000', '2026-09-01 00:00:00.000000', '2026-09-01 01:00:00.000000');

-- These command labels and hashes describe fixture history, not a new HTTP contract.
-- retain_until is a fixed test value, not a production retention policy.
INSERT INTO command_idempotency (
    id, actor_user_id, actor_scope, command_name, idempotency_key, request_hash,
    batch_id, state, response_status, response_json, created_at, completed_at, retain_until
)
SELECT CONCAT('c1260000', SUBSTRING(id, 9)), created_by, CONCAT('user:', created_by),
       'CreateDraft', CONCAT('c1-fixture-create:', id), SHA2(CONCAT('c1-fixture-create:', id), 256),
       id, 'COMPLETED', 201,
       JSON_OBJECT('batchId', id, 'status', 'DRAFT', 'version', 1),
       created_at, created_at, '2027-09-01 00:00:00.000000'
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007');

INSERT INTO batch_audit_events (
    id, batch_id, command_id, actor_user_id, actor_org_id, event_type,
    from_status, to_status, batch_version, sequence_in_command,
    occurred_at, correlation_id, details_json
)
SELECT CONCAT('a1260000', SUBSTRING(id, 9)), id, CONCAT('c1260000', SUBSTRING(id, 9)),
       created_by, organization_id, 'DraftSaved', 'DRAFT', 'DRAFT', 1, 1,
       created_at, CONCAT('c1-fixture-create:', id), JSON_OBJECT('operation', 'CREATE')
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007');

INSERT INTO command_idempotency (
    id, actor_user_id, actor_scope, command_name, idempotency_key, request_hash,
    batch_id, state, response_status, response_json, created_at, completed_at, retain_until
)
SELECT CONCAT('c1260001', SUBSTRING(id, 9)), created_by, CONCAT('user:', created_by),
       'SubmitBatch', CONCAT('c1-fixture-submit:', id), SHA2(CONCAT('c1-fixture-submit:', id), 256),
       id, 'COMPLETED', 200,
       JSON_OBJECT('batchId', id, 'status', 'SUBMITTED', 'version', 2,
                   'eventId', CONCAT('e1260000', SUBSTRING(id, 9)), 'eventState', 'PENDING'),
       submitted_at, submitted_at, '2027-09-01 00:00:00.000000'
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007') AND status = 'SUBMITTED';

INSERT INTO batch_audit_events (
    id, batch_id, command_id, actor_user_id, actor_org_id, event_type,
    from_status, to_status, batch_version, sequence_in_command,
    occurred_at, correlation_id, details_json
)
SELECT CONCAT('a1260001', SUBSTRING(id, 9)), id, CONCAT('c1260001', SUBSTRING(id, 9)),
       created_by, organization_id, 'RequestSubmitted', 'DRAFT', 'SUBMITTED', version, 1,
       submitted_at, CONCAT('c1-fixture-submit:', id), JSON_OBJECT('operation', 'SUBMIT')
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007') AND status = 'SUBMITTED';

INSERT INTO event_outbox (
    event_id, batch_id, command_id, event_type, topic, schema_version,
    aggregate_version, sequence_in_command, partition_key, payload_json,
    correlation_id, occurred_at, created_at, publish_state, attempt_count, next_attempt_at
)
SELECT CONCAT('e1260000', SUBSTRING(id, 9)), id, CONCAT('c1260001', SUBSTRING(id, 9)),
       'RequestSubmitted', 'ewaste.batch.events', 1, version, 1, id,
       JSON_OBJECT(
           'event_id', CONCAT('e1260000', SUBSTRING(id, 9)), 'event_type', 'RequestSubmitted',
           'schema_version', 1, 'command_id', CONCAT('c1260001', SUBSTRING(id, 9)),
           'batch_id', id, 'batch_version', version, 'claim_epoch', CAST(claim_epoch AS CHAR),
           'sequence_in_command', 1, 'occurred_at', DATE_FORMAT(submitted_at, '%Y-%m-%dT%H:%i:%s.%fZ'),
           'correlation_id', CONCAT('c1-fixture-submit:', id),
           'data', JSON_OBJECT(
               'organization_id', organization_id,
               'submitted_at', DATE_FORMAT(submitted_at, '%Y-%m-%dT%H:%i:%s.%fZ'),
               'category', category, 'quantity', quantity,
               'estimated_weight_kg', CAST(estimated_weight_kg AS CHAR),
               'condition_rating', condition_rating,
               'is_data_bearing', JSON_EXTRACT(IF(is_data_bearing, 'true', 'false'), '$'),
               'zone', zone, 'collection_deadline', DATE_FORMAT(collection_deadline, '%Y-%m-%dT%H:%i:%s.%fZ'))),
       CONCAT('c1-fixture-submit:', id), submitted_at, submitted_at, 'PENDING', 0, submitted_at
FROM ewaste_batches WHERE id IN (
    'b1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000002',
    'b1260000-0000-4000-8000-000000000003',
    'b1260000-0000-4000-8000-000000000004',
    'b1260000-0000-4000-8000-000000000005',
    'b1260000-0000-4000-8000-000000000006',
    'b1260000-0000-4000-8000-000000000007') AND status = 'SUBMITTED';

-- No destructive rollback: fixture command/audit/outbox history is retained.
-- The test runner disposes only of its own isolated container volume.
PERSISTENCE_EMBED_011

}

run_c1() {
  EVIDENCE_DIR="$RUN_DIR/sprint1-c1"
  CHANGELOG="tests/c1/changelog-c1.yaml"
  mkdir -p "$EVIDENCE_DIR"
  printf '\nRunning sprint1-c1 checks from the embedded tests.\n'
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  echo '[1/6] Verifying a clean migration with all synthetic seeds excluded.'
  lb c1_clean validate validate
  lb c1_clean update-sql update-sql
  lb c1_clean schema update
  check_scalar c1_clean schema_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 10
  check_scalar c1_clean seed_excluded_users 'SELECT COUNT(*) FROM users' 0
  check_scalar c1_clean seed_excluded_batches 'SELECT COUNT(*) FROM ewaste_batches' 0
  lb c1_clean identity-seed update --context-filter=seed
  check_scalar c1_clean ordinary_seed_excludes_c1 'SELECT COUNT(*) FROM ewaste_batches' 0

  echo '[2/6] Upgrading Sprint 1 while preserving its rows and changelog records.'
  lb c1_upgrade baseline --changelog-file=tests/c1/changelog-sprint1.yaml update --context-filter=seed
  check_scalar c1_upgrade baseline_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 8
  dump_rows c1_upgrade "$EVIDENCE_DIR/identity-before.sql" organisations roles users sessions login_audit
  mysql_query c1_upgrade -e 'SELECT ID, AUTHOR, FILENAME, MD5SUM, DATEEXECUTED FROM DATABASECHANGELOG ORDER BY ID' > "$EVIDENCE_DIR/baseline-before.tsv"
  lb c1_upgrade validate validate
  lb c1_upgrade update-sql update-sql --context-filter=seed
  lb c1_upgrade upgrade update --context-filter=seed
  check_scalar c1_upgrade upgraded_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 13
  dump_rows c1_upgrade "$EVIDENCE_DIR/identity-after.sql" organisations roles users sessions login_audit
  diff -u "$EVIDENCE_DIR/identity-before.sql" "$EVIDENCE_DIR/identity-after.sql" > "$EVIDENCE_DIR/identity-diff.txt"
  mysql_query c1_upgrade -e "SELECT ID, AUTHOR, FILENAME, MD5SUM, DATEEXECUTED FROM DATABASECHANGELOG WHERE ID LIKE 'EW101-%' ORDER BY ID" > "$EVIDENCE_DIR/baseline-after.tsv"
  diff -u "$EVIDENCE_DIR/baseline-before.tsv" "$EVIDENCE_DIR/baseline-after.tsv" > "$EVIDENCE_DIR/baseline-diff.txt"

  echo '[3/6] Applying deterministic fixtures and proving repeat update is a no-op.'
  for db in c1_clean c1_upgrade; do
    mysql_query "$db" < database/tests/verify-ew101-c1.sql > "$EVIDENCE_DIR/$db-sprint1.tsv"
    awk -F '\t' 'NR > 1 && $2 != "PASS" { bad=1 } END { exit bad }' "$EVIDENCE_DIR/$db-sprint1.tsv"
    lb "$db" fixtures update --context-filter=seed,c1-fixtures
    check_scalar "$db" "${db}_fixture_changes" 'SELECT COUNT(*) FROM DATABASECHANGELOG' 14
    check_scalar "$db" "${db}_fixture_rows" 'SELECT COUNT(*) FROM ewaste_batches' 7
    dump_rows "$db" "$EVIDENCE_DIR/$db-before.sql" ewaste_batches command_idempotency batch_audit_events event_outbox DATABASECHANGELOG
    lb "$db" repeat update --context-filter=seed,c1-fixtures
    dump_rows "$db" "$EVIDENCE_DIR/$db-after.sql" ewaste_batches command_idempotency batch_audit_events event_outbox DATABASECHANGELOG
    diff -u "$EVIDENCE_DIR/$db-before.sql" "$EVIDENCE_DIR/$db-after.sql" > "$EVIDENCE_DIR/$db-repeat-diff.txt"
  done

  echo '[4/6] Restarting MySQL to verify committed fixture persistence.'
  "${COMPOSE[@]}" restart mysql > "$EVIDENCE_DIR/restart.log" 2>&1
  "${COMPOSE[@]}" up -d --wait --wait-timeout 180 mysql >> "$EVIDENCE_DIR/restart.log" 2>&1
  for db in c1_clean c1_upgrade; do
    dump_rows "$db" "$EVIDENCE_DIR/$db-restarted.sql" ewaste_batches command_idempotency batch_audit_events event_outbox DATABASECHANGELOG
    diff -u "$EVIDENCE_DIR/$db-after.sql" "$EVIDENCE_DIR/$db-restarted.sql" > "$EVIDENCE_DIR/$db-restart-diff.txt"
  done

  echo '[5/6] Checking schema, valid/rejected records and transaction boundaries.'
  for db in c1_clean c1_upgrade; do
    mysql_query "$db" < database/tests/c1/verify.sql > "$EVIDENCE_DIR/$db-checks.tsv" 2> "$EVIDENCE_DIR/$db-checks-errors.log" || {
      awk -F '\t' 'NR == 1 || $2 == "FAIL"' "$EVIDENCE_DIR/$db-checks.tsv" >&2
      cat "$EVIDENCE_DIR/$db-checks-errors.log" >&2
      exit 1
    }
    awk -F '\t' 'NR > 1 { count++ } END { printf "%d SQL checks passed.\n", count }' "$EVIDENCE_DIR/$db-checks.tsv"
    dump_rows "$db" "$EVIDENCE_DIR/$db-final.sql" ewaste_batches command_idempotency batch_audit_events event_outbox
  done
  diff -u "$EVIDENCE_DIR/c1_clean-final.sql" "$EVIDENCE_DIR/c1_upgrade-final.sql" > "$EVIDENCE_DIR/clean-upgrade-diff.txt"

  echo '[6/6] All migration, persistence and rejection checks passed.'
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'sprint1-c1\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

run_c3() {
  EVIDENCE_DIR="$RUN_DIR/c2-c3"
  CHANGELOG="tests/c3/changelog-c3.yaml"
  mkdir -p "$EVIDENCE_DIR"
  printf '\nRunning c2-c3 checks from the embedded tests.\n'
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  echo '[1/6] Verifying clean C3 migrations.'
  lb c3_clean validate validate
  lb c3_clean preview update-sql
  lb c3_clean schema update
  check_scalar c3_clean schema_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 21
  check_scalar c3_clean seed_excluded 'SELECT COUNT(*) FROM users' 0
  check_scalar c3_clean claims_empty 'SELECT COUNT(*) FROM batch_claims' 0
  lb c3_clean identities update --context-filter=seed
  check_scalar c3_clean c1_fixtures_opt_in 'SELECT COUNT(*) FROM ewaste_batches' 0

  echo '[2/6] Upgrading existing C1 fixtures while preserving all rows and changelog identities.'
  lb c3_upgrade c1-baseline --changelog-file=tests/c1/changelog-c1.yaml update --context-filter=seed,c1-fixtures
  check_scalar c3_upgrade c1_baseline_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 14
  C1_TABLES=(organisations roles users sessions login_audit ewaste_batches command_idempotency batch_audit_events event_outbox)
  dump_rows c3_upgrade "$EVIDENCE_DIR/c1-before.sql" "${C1_TABLES[@]}"
  mysql_query c3_upgrade -e 'SELECT ID,AUTHOR,FILENAME,MD5SUM,DATEEXECUTED FROM DATABASECHANGELOG ORDER BY ID' > "$EVIDENCE_DIR/changelog-before.tsv"
  lb c3_upgrade validate validate
  lb c3_upgrade preview update-sql --context-filter=seed,c1-fixtures
  lb c3_upgrade upgrade update --context-filter=seed,c1-fixtures
  check_scalar c3_upgrade upgraded_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 25
  dump_rows c3_upgrade "$EVIDENCE_DIR/c1-after.sql" "${C1_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/c1-before.sql" "$EVIDENCE_DIR/c1-after.sql" > "$EVIDENCE_DIR/c1-preserved-diff.txt"
  mysql_query c3_upgrade -e "SELECT ID,AUTHOR,FILENAME,MD5SUM,DATEEXECUTED FROM DATABASECHANGELOG WHERE ID NOT LIKE 'EWCSB2-%' AND ID NOT LIKE 'EWCSB3-%' ORDER BY ID" > "$EVIDENCE_DIR/changelog-after.tsv"
  diff -u "$EVIDENCE_DIR/changelog-before.tsv" "$EVIDENCE_DIR/changelog-after.tsv" > "$EVIDENCE_DIR/changelog-preserved-diff.txt"

  echo '[3/6] Checking valid/rejected records, unique keys and foreign keys on both paths.'
  for db in c3_clean c3_upgrade; do
    mysql_query "$db" < database/tests/c3/schema-fixtures.sql
    mysql_query "$db" < database/tests/c3/verify-schema.sql > "$EVIDENCE_DIR/$db-schema.tsv" 2> "$EVIDENCE_DIR/$db-schema-errors.txt" || {
      awk -F '\t' 'NR==1 || $2=="FAIL"' "$EVIDENCE_DIR/$db-schema.tsv" >&2
      cat "$EVIDENCE_DIR/$db-schema-errors.txt" >&2; exit 1;
    }
    awk -F '\t' 'NR>1 {n++} END {printf "%d SQL schema checks passed.\n",n}' "$EVIDENCE_DIR/$db-schema.tsv"
  done

  echo '[4/6] Checking concurrent SQL claims and atomic rollback.'
  run_claim_sql_concurrency

  echo '[5/6] Reapplying migrations and restarting MySQL with committed claim history.'
  DOMAIN_TABLES=("${C1_TABLES[@]}" matching_rule_sets recycler_matching_profiles recycler_capacity_pools recycler_category_capabilities recycler_service_zones matching_decisions matched_results batch_claims capacity_reservations DATABASECHANGELOG)
  for db in c3_clean c3_upgrade; do
    dump_rows "$db" "$EVIDENCE_DIR/$db-before-repeat.sql" "${DOMAIN_TABLES[@]}"
    if [[ "$db" == c3_clean ]]; then lb "$db" repeat update --context-filter=seed; else lb "$db" repeat update --context-filter=seed,c1-fixtures; fi
    dump_rows "$db" "$EVIDENCE_DIR/$db-after-repeat.sql" "${DOMAIN_TABLES[@]}"
    diff -u "$EVIDENCE_DIR/$db-before-repeat.sql" "$EVIDENCE_DIR/$db-after-repeat.sql" > "$EVIDENCE_DIR/$db-repeat-diff.txt"
  done
  "${COMPOSE[@]}" restart mysql > "$EVIDENCE_DIR/restart.txt" 2>&1
  "${COMPOSE[@]}" up -d --wait --wait-timeout 180 mysql >> "$EVIDENCE_DIR/restart.txt" 2>&1
  for db in c3_clean c3_upgrade; do
    dump_rows "$db" "$EVIDENCE_DIR/$db-after-restart.sql" "${DOMAIN_TABLES[@]}"
    diff -u "$EVIDENCE_DIR/$db-after-repeat.sql" "$EVIDENCE_DIR/$db-after-restart.sql" > "$EVIDENCE_DIR/$db-restart-diff.txt"
  done

  echo '[6/6] All C3 migration, schema and SQL persistence checks passed.'
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'c2-c3\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

run_c4() {
  EVIDENCE_DIR="$RUN_DIR/c4"
  CHANGELOG="changelog-before-recycler-config.yaml"
  mkdir -p "$EVIDENCE_DIR"
  printf '\nRunning c4 checks from the embedded tests.\n'
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  echo '[1/6] Verifying clean C4 migrations.'
  lb c4_clean validate validate
  lb c4_clean preview update-sql
  lb c4_clean schema update
  check_scalar c4_clean schema_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 28
  check_scalar c4_clean seed_excluded 'SELECT COUNT(*) FROM users' 0
  check_scalar c4_clean matching_seed_excluded 'SELECT COUNT(*) FROM matching_rule_sets' 0
  check_scalar c4_clean repair_excluded_without_seed "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106'" 0
  check_scalar c4_clean collector_seed_excluded 'SELECT COUNT(*) FROM recycler_collector_scopes' 0
  check_scalar c4_clean claims_empty 'SELECT COUNT(*) FROM batch_claims' 0
  reject_matching_seed c4_clean matching_seed_missing_creator
  lb c4_clean identities update --context-filter=seed
  check_scalar c4_clean unaffected_repair_recorded "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106' AND EXECTYPE='EXECUTED'" 1
  check_scalar c4_clean c1_fixtures_opt_in 'SELECT COUNT(*) FROM ewaste_batches' 0
  check_scalar c4_clean matching_seed_active "SELECT COUNT(*) FROM matching_rule_sets WHERE version='binary-v1' AND created_by='USR-001' AND effective_from=created_at AND effective_from<=UTC_TIMESTAMP(6) AND retired_at IS NULL" 1
  check_scalar c4_clean collector_seed_pairs "SELECT GROUP_CONCAT(CONCAT(recycler_org_id,':',collector_org_id,':',zone) ORDER BY recycler_org_id,collector_org_id) FROM recycler_collector_scopes" 'PROC-001:COL-001:NORTH,PROC-001:COL-002:NORTH,PROC-002:COL-001:EAST'
  check_scalar c4_clean collector_seed_activation "SELECT COUNT(*) FROM recycler_collector_scopes WHERE is_active=1 AND version=1 AND valid_from=created_at AND updated_at=created_at AND valid_from<=UTC_TIMESTAMP(6) AND valid_until IS NULL" 3
  # An already-applied original 105 checksum must remain valid and unchanged.
  mysql_query c4_clean -e "UPDATE DATABASECHANGELOG SET MD5SUM='9:e6bf826ee57fbbec596986f0f65ffe25' WHERE ID='EWCSB129-105'"
  dump_rows c4_clean "$EVIDENCE_DIR/original-105-before.sql" matching_rule_sets DATABASECHANGELOG
  lb c4_clean original-checksum validate
  lb c4_clean original-checksum-repeat update --context-filter=seed
  dump_rows c4_clean "$EVIDENCE_DIR/original-105-after.sql" matching_rule_sets DATABASECHANGELOG
  diff -u "$EVIDENCE_DIR/original-105-before.sql" "$EVIDENCE_DIR/original-105-after.sql" > "$EVIDENCE_DIR/original-105-preserved-diff.txt"

  # Reuse the isolated, completed C3 database to verify an identical manual
  # policy with a different valid ID/creator is adopted without changing it.
  mysql_query c3_clean -e "INSERT INTO matching_rule_sets SELECT 'a1299999-0000-4000-8000-000000000002',version,rules_json,'2026-01-01',NULL,'USR-002','2026-01-01' FROM c4_clean.matching_rule_sets WHERE version='binary-v1'"
  dump_rows c3_clean "$EVIDENCE_DIR/manual-policy-before.sql" matching_rule_sets
  lb c3_clean adopt-manual-policy --changelog-file=seed/105-seed-matching-rule-sets.sql update --context-filter=seed
  dump_rows c3_clean "$EVIDENCE_DIR/manual-policy-after.sql" matching_rule_sets
  diff -u "$EVIDENCE_DIR/manual-policy-before.sql" "$EVIDENCE_DIR/manual-policy-after.sql" > "$EVIDENCE_DIR/manual-policy-preserved-diff.txt"
  check_scalar c3_clean manual_policy_adopted "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-105' AND EXECTYPE='EXECUTED'" 1

  echo '[2/6] Upgrading existing C3 claims while preserving all rows and changelog identities.'
  lb c4_upgrade c3-baseline --changelog-file=tests/c3/changelog-c3.yaml update --context-filter=seed,c1-fixtures
  check_scalar c4_upgrade c3_baseline_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 25
  mysql_query c4_upgrade < database/tests/c3/schema-fixtures.sql
  mysql_query c4_upgrade -e "UPDATE users SET status='DISABLED' WHERE user_id='USR-001'"
  reject_matching_seed c4_upgrade matching_seed_inactive_creator
  mysql_query c4_upgrade -e "UPDATE users SET status='ACTIVE' WHERE user_id='USR-001'"
  mysql_query c4_upgrade -e "INSERT INTO matching_rule_sets(id,version,rules_json,effective_from,created_by,created_at) VALUES('a1299999-0000-4000-8000-000000000001','overlap-test',JSON_OBJECT(),UTC_TIMESTAMP(6)+INTERVAL 1 DAY,'USR-001',UTC_TIMESTAMP(6))"
  reject_matching_seed c4_upgrade matching_seed_overlapping_policy
  mysql_query c4_upgrade -e "UPDATE matching_rule_sets SET version='binary-v1',effective_from='2026-01-01',retired_at='2026-01-02' WHERE version='overlap-test'"
  reject_matching_seed c4_upgrade matching_seed_existing_version
  mysql_query c4_upgrade -e "DELETE FROM matching_rule_sets WHERE id='a1299999-0000-4000-8000-000000000001'"
  # Reproduce the reported dev row, including its invalid UUID and old dates.
  mysql_query c4_upgrade -e "INSERT INTO matching_rule_sets SELECT 'r2260000-0000-4000-8000-000000000001',version,rules_json,'2026-01-01',NULL,'USR-001','2026-01-01' FROM c4_clean.matching_rule_sets WHERE version='binary-v1'"
  mysql_query c4_upgrade -e "UPDATE matching_rule_sets SET rules_json=JSON_SET(rules_json,'$.ranking',TRUE) WHERE version='binary-v1'"
  reject_matching_seed c4_upgrade matching_seed_different_content
  mysql_query c4_upgrade -e "UPDATE matching_rule_sets SET rules_json=(SELECT rules_json FROM c4_clean.matching_rule_sets WHERE version='binary-v1'),effective_from=UTC_TIMESTAMP(6)+INTERVAL 1 DAY WHERE version='binary-v1'"
  reject_matching_seed c4_upgrade matching_seed_future_policy
  mysql_query c4_upgrade -e "UPDATE matching_rule_sets SET effective_from='2026-01-01',id='x2260000-0000-4000-8000-000000000001' WHERE version='binary-v1'"
  reject_matching_seed c4_upgrade matching_seed_unknown_invalid_id
  mysql_query c4_upgrade -e "UPDATE matching_rule_sets SET id='r2260000-0000-4000-8000-000000000001' WHERE version='binary-v1'"
  mysql_query c4_upgrade -e "INSERT INTO matching_rule_sets(id,version,rules_json,effective_from,retired_at,created_by,created_at) VALUES('a1290000-0000-4000-8000-000000000001','retired-collision',JSON_OBJECT(),'2026-01-01','2026-01-02','USR-001','2026-01-01')"
  reject_matching_seed c4_upgrade matching_seed_id_collision
  mysql_query c4_upgrade -e "DELETE FROM matching_rule_sets WHERE version='retired-collision'"
  local table column
  for table in command_idempotency event_outbox batch_audit_events; do
    case "$table" in
      command_idempotency) column=response_json ;;
      event_outbox) column=payload_json ;;
      batch_audit_events) column=details_json ;;
    esac
    mysql_query c4_upgrade -e "UPDATE $table SET $column=JSON_SET($column,'$.seed_reference_fixture','r2260000-0000-4000-8000-000000000001')"
    reject_matching_seed c4_upgrade "matching_seed_referenced_by_$table"
    mysql_query c4_upgrade -e "UPDATE $table SET $column=JSON_REMOVE($column,'$.seed_reference_fixture')"
  done
  mysql_query c4_upgrade <<'SQL_REFERENCED_POLICY'
INSERT INTO matching_decisions(id,batch_id,trigger_id,trigger_type,batch_version,claim_epoch,rule_set_id,evaluation_at,input_hash,profile_snapshot_hash,input_snapshot_json,outcome,primary_reason,evaluated_count,eligible_count,correlation_id,created_at,completed_at)
VALUES('a1299999-0000-4000-8000-000000000003','b3000000-0000-4000-8000-000000000001','a1299999-0000-4000-8000-000000000004','EXPLICIT_RUN',3,1,'r2260000-0000-4000-8000-000000000001','2026-09-18',REPEAT('a',64),REPEAT('b',64),JSON_OBJECT(),'NO_MATCH','NO_APPROVED_ORGANISATION',0,0,'seed-reference-fixture','2026-09-18','2026-09-18');
SQL_REFERENCED_POLICY
  reject_matching_seed c4_upgrade matching_seed_referenced_by_decision
  mysql_query c4_upgrade -e "DELETE FROM matching_decisions WHERE id='a1299999-0000-4000-8000-000000000003'"
  C3_TABLES=(organisations roles users sessions login_audit ewaste_batches command_idempotency batch_audit_events event_outbox recycler_matching_profiles recycler_capacity_pools recycler_category_capabilities recycler_service_zones matching_decisions matched_results batch_claims capacity_reservations)
  dump_rows c4_upgrade "$EVIDENCE_DIR/c3-before.sql" "${C3_TABLES[@]}"
  dump_rows c4_upgrade "$EVIDENCE_DIR/c3-rules-before.sql" matching_rule_sets --where="id <> 'r2260000-0000-4000-8000-000000000001'"
  mysql_query c4_upgrade -e "SELECT version,rules_json,effective_from,retired_at,created_by,created_at FROM matching_rule_sets WHERE version='binary-v1'" > "$EVIDENCE_DIR/repaired-policy-before.tsv"
  mysql_query c4_upgrade -e 'SELECT ID,AUTHOR,FILENAME,MD5SUM,DATEEXECUTED FROM DATABASECHANGELOG ORDER BY ID' > "$EVIDENCE_DIR/changelog-before.tsv"
  lb c4_upgrade validate validate
  lb c4_upgrade preview update-sql --context-filter=seed,c1-fixtures
  lb c4_upgrade upgrade update --context-filter=seed,c1-fixtures
  check_scalar c4_upgrade upgraded_changes 'SELECT COUNT(*) FROM DATABASECHANGELOG' 35
  check_scalar c4_upgrade matching_seed_active "SELECT COUNT(*) FROM matching_rule_sets WHERE version='binary-v1' AND created_by='USR-001' AND effective_from=created_at AND effective_from<=UTC_TIMESTAMP(6) AND retired_at IS NULL" 1
  check_scalar c4_upgrade matching_seed_uuid_repaired "SELECT COUNT(*) FROM matching_rule_sets WHERE version='binary-v1' AND id='a1290000-0000-4000-8000-000000000001'" 1
  mysql_query c4_upgrade -e "SELECT version,rules_json,effective_from,retired_at,created_by,created_at FROM matching_rule_sets WHERE version='binary-v1'" > "$EVIDENCE_DIR/repaired-policy-after.tsv"
  diff -u "$EVIDENCE_DIR/repaired-policy-before.tsv" "$EVIDENCE_DIR/repaired-policy-after.tsv" > "$EVIDENCE_DIR/repaired-policy-preserved-diff.txt"
  dump_rows c4_upgrade "$EVIDENCE_DIR/c3-after.sql" "${C3_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/c3-before.sql" "$EVIDENCE_DIR/c3-after.sql" > "$EVIDENCE_DIR/c3-preserved-diff.txt"
  dump_rows c4_upgrade "$EVIDENCE_DIR/c3-rules-after.sql" matching_rule_sets --where="id <> 'a1290000-0000-4000-8000-000000000001'"
  diff -u "$EVIDENCE_DIR/c3-rules-before.sql" "$EVIDENCE_DIR/c3-rules-after.sql" > "$EVIDENCE_DIR/c3-rules-preserved-diff.txt"
  mysql_query c4_upgrade -e "SELECT ID,AUTHOR,FILENAME,MD5SUM,DATEEXECUTED FROM DATABASECHANGELOG WHERE ID NOT LIKE 'EWCSB4-%' AND ID NOT IN ('EWCSB129-105','EWCSB129-106') ORDER BY ID" > "$EVIDENCE_DIR/changelog-after.tsv"
  diff -u "$EVIDENCE_DIR/changelog-before.tsv" "$EVIDENCE_DIR/changelog-after.tsv" > "$EVIDENCE_DIR/changelog-preserved-diff.txt"
  mysql_query c4_clean < database/tests/c3/schema-fixtures.sql

  echo '[3/6] Checking valid/rejected records, unique keys and foreign keys on both paths.'
  for db in c4_clean c4_upgrade; do
    mysql_query "$db" < database/tests/c4/schema-fixtures.sql
    mysql_query "$db" < database/tests/c4/verify-schema.sql > "$EVIDENCE_DIR/$db-schema.tsv" 2> "$EVIDENCE_DIR/$db-schema-errors.txt" || {
      awk -F '\t' 'NR==1 || $2=="FAIL"' "$EVIDENCE_DIR/$db-schema.tsv" >&2
      cat "$EVIDENCE_DIR/$db-schema-errors.txt" >&2; exit 1;
    }
    awk -F '\t' 'NR>1 {n++} END {printf "%d SQL schema checks passed.\n",n}' "$EVIDENCE_DIR/$db-schema.tsv"
  done

  echo '[4/6] Checking concurrent SQL assignments and handoff outcomes.'
  run_assignment_sql_concurrency

  # Persisted SQL fixture evidence; no application command execution is claimed.
  mysql_query c4_clean -e "SELECT * FROM batch_assignments ORDER BY batch_id,assignment_sequence" > "$EVIDENCE_DIR/schema-assignments.tsv"
  mysql_query c4_clean -e "SELECT * FROM batch_handoffs ORDER BY batch_id,id" > "$EVIDENCE_DIR/schema-handoffs.tsv"

  echo '[5/6] Reapplying migrations and restarting MySQL with committed assignment history.'
  DOMAIN_TABLES=("${C3_TABLES[@]}" matching_rule_sets recycler_collector_scopes batch_assignments batch_handoffs assignment_actions DATABASECHANGELOG)
  for db in c4_clean c4_upgrade; do
    dump_rows "$db" "$EVIDENCE_DIR/$db-before-repeat.sql" "${DOMAIN_TABLES[@]}"
    if [[ "$db" == c4_clean ]]; then lb "$db" repeat update --context-filter=seed; else lb "$db" repeat update --context-filter=seed,c1-fixtures; fi
    dump_rows "$db" "$EVIDENCE_DIR/$db-after-repeat.sql" "${DOMAIN_TABLES[@]}"
    diff -u "$EVIDENCE_DIR/$db-before-repeat.sql" "$EVIDENCE_DIR/$db-after-repeat.sql" > "$EVIDENCE_DIR/$db-repeat-diff.txt"
  done
  "${COMPOSE[@]}" restart mysql > "$EVIDENCE_DIR/restart.txt" 2>&1
  "${COMPOSE[@]}" up -d --wait --wait-timeout 180 mysql >> "$EVIDENCE_DIR/restart.txt" 2>&1
  for db in c4_clean c4_upgrade; do
    dump_rows "$db" "$EVIDENCE_DIR/$db-after-restart.sql" "${DOMAIN_TABLES[@]}"
    diff -u "$EVIDENCE_DIR/$db-after-repeat.sql" "$EVIDENCE_DIR/$db-after-restart.sql" > "$EVIDENCE_DIR/$db-restart-diff.txt"
  done

  echo '[6/6] All C4 migration, schema and SQL persistence checks passed.'
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'c4\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

reject_config_repair() {
  local name="$1" expected="$2"
  dump_rows matcher_repair "$EVIDENCE_DIR/$name-before.sql" "${REPAIR_TABLES[@]}"
  if lb matcher_repair "$name" update --context-filter=seed; then
    echo "FAIL: configuration repair unexpectedly succeeded: $name" >&2; return 1
  fi
  grep -Fq "$expected" "$EVIDENCE_DIR/matcher_repair-$name.log"
  check_scalar matcher_repair "$name-not-recorded" "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106'" 0
  dump_rows matcher_repair "$EVIDENCE_DIR/$name-after.sql" "${REPAIR_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/$name-before.sql" "$EVIDENCE_DIR/$name-after.sql" > "$EVIDENCE_DIR/$name-diff.txt"
}

run_matcher_repair() {
  EVIDENCE_DIR="$RUN_DIR/matcher-repair"
  CHANGELOG="changelog-before-collector-scopes.yaml"
  mkdir -p "$EVIDENCE_DIR"
  printf '\nChecking the reported dev configuration repair and rejected histories.\n'
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  # Reproduce a database upgraded through 105 before the repair was introduced.
  # Keep include paths/changeset identities identical to the production master.
  awk '/^  - include:/ {block=$0 ORS; next} {block=block $0 ORS; if (/relativeToChangelogFile:/) {if (block !~ /106-repair-matching-config-ids/) printf "%s",block; block=""}} NR==1 {printf "%s",block; block=""}' \
    database/changelog-before-collector-scopes.yaml > database/changelog-before-config-repair.yaml
  lb matcher_repair migrate --changelog-file=changelog-before-config-repair.yaml update --context-filter=seed
  check_scalar matcher_repair repair_pending "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106'" 0
  REPAIR_TABLES=(recycler_matching_profiles recycler_capacity_pools recycler_category_capabilities recycler_service_zones
    ewaste_batches command_idempotency matching_decisions matched_results batch_claims capacity_reservations event_outbox batch_audit_events)
  mysql_query matcher_repair <<'REPAIR_FIXTURES'
INSERT INTO recycler_matching_profiles VALUES
('PROC-001',1,1,'2026-01-01','2026-01-01'),('PROC-002',1,1,'2026-01-01','2026-01-01');
INSERT INTO recycler_capacity_pools VALUES
('p2260000-0000-4000-8000-000000000001','PROC-001','MAIN',50000,0,1,1,'2026-01-01'),
('p2260000-0000-4000-8000-000000000002','PROC-002','MAIN',50000,0,1,1,'2026-01-01');
INSERT INTO recycler_category_capabilities
SELECT CONCAT('c',(o.n-1)*4+c.n),CONCAT('PROC-00',o.n),c.category,
       '["FUNCTIONAL","REPAIRABLE","END_OF_LIFE"]',c.data_bearing,1,
       CONCAT('p2260000-0000-4000-8000-00000000000',o.n),1,'2026-01-01'
FROM (SELECT 1 n UNION ALL SELECT 2) o CROSS JOIN
     (SELECT 1 n,'ICT_EQUIPMENT' category,1 data_bearing UNION ALL
      SELECT 2,'LARGE_APPLIANCE',0 UNION ALL SELECT 3,'BATTERIES',0 UNION ALL SELECT 4,'CONSUMER_ELECTRONICS',1) c;
INSERT INTO recycler_service_zones
SELECT CONCAT('z',(o.n-1)*5+z.n),CONCAT('PROC-00',o.n),z.zone,0,1,1,'2026-01-01'
FROM (SELECT 1 n UNION ALL SELECT 2) o CROSS JOIN
     (SELECT 1 n,'NORTH' zone UNION ALL SELECT 2,'SOUTH' UNION ALL SELECT 3,'EAST'
      UNION ALL SELECT 4,'WEST' UNION ALL SELECT 5,'CENTRAL') z;
-- SQL-only carriers for history guards; not application-generated outcomes.
INSERT INTO ewaste_batches(id,organization_id,created_by)
VALUES('fe000000-0000-4000-8000-000000000001','DON-001','USR-003');
INSERT INTO command_idempotency(id,service_principal,actor_scope,command_name,idempotency_key,request_hash,batch_id,response_json,created_at,retain_until)
VALUES('fe000000-0000-4000-8000-000000000002','repair-test','service:repair-test','RepairFixture','repair-fixture-001',REPEAT('a',64),'fe000000-0000-4000-8000-000000000001',JSON_OBJECT(),'2026-01-01','2027-01-01');
INSERT INTO event_outbox(event_id,batch_id,command_id,event_type,topic,schema_version,aggregate_version,sequence_in_command,partition_key,payload_json,correlation_id,occurred_at,next_attempt_at)
VALUES('fe000000-0000-4000-8000-000000000003','fe000000-0000-4000-8000-000000000001','fe000000-0000-4000-8000-000000000002','RepairFixture','test',1,1,1,'fe000000-0000-4000-8000-000000000001',JSON_OBJECT(),'repair-test','2026-01-01','2026-01-01');
INSERT INTO batch_audit_events(id,batch_id,command_id,service_principal,event_type,from_status,to_status,batch_version,sequence_in_command,occurred_at,correlation_id,details_json)
VALUES('fe000000-0000-4000-8000-000000000004','fe000000-0000-4000-8000-000000000001','fe000000-0000-4000-8000-000000000002','repair-test','RepairFixture','DRAFT','DRAFT',1,1,'2026-01-01','repair-test',JSON_OBJECT());
INSERT INTO matching_decisions(id,batch_id,trigger_id,trigger_type,batch_version,claim_epoch,rule_set_id,evaluation_at,input_hash,profile_snapshot_hash,input_snapshot_json,outcome,primary_reason,evaluated_count,eligible_count,correlation_id,created_at,completed_at)
VALUES('fe000000-0000-4000-8000-000000000005','fe000000-0000-4000-8000-000000000001','fe000000-0000-4000-8000-000000000006','EXPLICIT_RUN',1,1,'a1290000-0000-4000-8000-000000000001','2026-01-01',REPEAT('a',64),REPEAT('b',64),JSON_OBJECT(),'NO_MATCH','NO_ELIGIBLE_ORGANISATION',1,0,'repair-test','2026-01-01','2026-01-01');
INSERT INTO matched_results(id,decision_id,batch_id,recycler_org_id,category_match,capability_match,capacity_available,zone_match,deadline_viable,is_matched,reason_code,failed_rules_json,evidence_json,created_at)
VALUES('fe000000-0000-4000-8000-000000000007','fe000000-0000-4000-8000-000000000005','fe000000-0000-4000-8000-000000000001','PROC-001',0,0,0,0,0,0,'CATEGORY_UNSUPPORTED','[{}]',JSON_OBJECT(),'2026-01-01');
INSERT INTO batch_claims(id,batch_id,recycler_org_id,claimed_by,idempotency_key)
VALUES('fe000000-0000-4000-8000-000000000008','fe000000-0000-4000-8000-000000000001','PROC-001','USR-007','repair-claim-fixture-001');
REPAIR_FIXTURES
  local table column spec
  for spec in 'command_idempotency response_json' 'matching_decisions input_snapshot_json' 'matched_results evidence_json' 'event_outbox payload_json' 'batch_audit_events details_json'; do
    read -r table column <<< "$spec"
    mysql_query matcher_repair -e "UPDATE $table SET $column=JSON_OBJECT('capability_id','c1')"
    reject_config_repair "history-$table" 'Historical or prepared records reference the old IDs'
    mysql_query matcher_repair -e "UPDATE $table SET $column=JSON_OBJECT()"
  done
  mysql_query matcher_repair -e "UPDATE matched_results SET capacity_pool_id='p2260000-0000-4000-8000-000000000001'"
  reject_config_repair matched_pool 'Historical or prepared records reference the old IDs'
  mysql_query matcher_repair -e 'UPDATE matched_results SET capacity_pool_id=NULL'
  mysql_query matcher_repair -e "INSERT INTO capacity_reservations(id,batch_id,claim_id,capacity_pool_id,reserved_kg,status,reserved_at,version) VALUES('fe000000-0000-4000-8000-000000000009','fe000000-0000-4000-8000-000000000001','fe000000-0000-4000-8000-000000000008','p2260000-0000-4000-8000-000000000001',1,'RESERVED','2026-01-01',1)"
  reject_config_repair reserved_history 'Historical or prepared records reference the old IDs'
  mysql_query matcher_repair -e 'DELETE FROM capacity_reservations'
  mysql_query matcher_repair -e 'UPDATE recycler_capacity_pools SET reserved_kg=1'
  reject_config_repair nonzero_reservation 'Reserved capacity'
  mysql_query matcher_repair -e 'UPDATE recycler_capacity_pools SET reserved_kg=0'
  mysql_query matcher_repair -e "UPDATE recycler_service_zones SET id='unexpected-zone-id' WHERE id='z10'"
  reject_config_repair partial_state 'Reported IDs, owners or configuration keys differ'
  mysql_query matcher_repair -e "UPDATE recycler_service_zones SET id='z10' WHERE id='unexpected-zone-id'"
  mysql_query matcher_repair -e "INSERT INTO recycler_capacity_pools VALUES('b2260000-0000-4000-8000-000000000001','PROC-001','COLLISION',1,0,1,1,'2026-01-01')"
  reject_config_repair collision 'Replacement UUID already exists'
  mysql_query matcher_repair -e "DELETE FROM recycler_capacity_pools WHERE pool_code='COLLISION'"
  # Fail after replacement pools and capability rows have been written.
  mysql_query matcher_repair -e "CREATE TABLE extra_pool_reference(id INT PRIMARY KEY,pool_id VARCHAR(36),FOREIGN KEY(pool_id) REFERENCES recycler_capacity_pools(id)); INSERT INTO extra_pool_reference VALUES(1,'p2260000-0000-4000-8000-000000000001')"
  reject_config_repair late_foreign_key 'Cannot delete or update a parent row'
  mysql_query matcher_repair -e 'DROP TABLE extra_pool_reference'

  local business_query="SELECT recycler_org_id,pool_code,total_kg,reserved_kg,is_active FROM recycler_capacity_pools ORDER BY recycler_org_id,pool_code; SELECT recycler_org_id,category,accepted_conditions_json,supports_data_bearing,is_active FROM recycler_category_capabilities ORDER BY recycler_org_id,category; SELECT recycler_org_id,zone,minimum_lead_minutes,is_active FROM recycler_service_zones ORDER BY recycler_org_id,zone;"
  mysql_query matcher_repair -e "$business_query" > "$EVIDENCE_DIR/business-before.tsv"
  dump_rows matcher_repair "$EVIDENCE_DIR/history-before.sql" "${REPAIR_TABLES[@]:4}" recycler_matching_profiles matching_rule_sets
  dump_rows matcher_repair "$EVIDENCE_DIR/changelog-before.sql" DATABASECHANGELOG
  lb matcher_repair repair update --context-filter=seed
  check_scalar matcher_repair repair_recorded "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106' AND EXECTYPE='EXECUTED'" 1
  dump_rows matcher_repair "$EVIDENCE_DIR/changelog-after.sql" DATABASECHANGELOG --where="ID <> 'EWCSB129-106'"
  diff -u "$EVIDENCE_DIR/changelog-before.sql" "$EVIDENCE_DIR/changelog-after.sql" > "$EVIDENCE_DIR/changelog-preserved.diff"
  mysql_query matcher_repair -e "$business_query" > "$EVIDENCE_DIR/business-after.tsv"
  diff -u "$EVIDENCE_DIR/business-before.tsv" "$EVIDENCE_DIR/business-after.tsv" > "$EVIDENCE_DIR/business-preserved.diff"
  dump_rows matcher_repair "$EVIDENCE_DIR/history-after.sql" "${REPAIR_TABLES[@]:4}" recycler_matching_profiles matching_rule_sets
  diff -u "$EVIDENCE_DIR/history-before.sql" "$EVIDENCE_DIR/history-after.sql" > "$EVIDENCE_DIR/history-preserved.diff"
  check_scalar matcher_repair repaired_versions "SELECT COUNT(*) FROM (SELECT version FROM recycler_capacity_pools UNION ALL SELECT version FROM recycler_category_capabilities UNION ALL SELECT version FROM recycler_service_zones) x WHERE version=2" 20
  check_scalar matcher_repair valid_ids "SELECT COUNT(*) FROM (SELECT id FROM recycler_capacity_pools UNION ALL SELECT id FROM recycler_category_capabilities UNION ALL SELECT id FROM recycler_service_zones) x WHERE REGEXP_LIKE(id,'^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$','c')" 20
  check_scalar matcher_repair owned_links 'SELECT COUNT(*) FROM recycler_category_capabilities c JOIN recycler_capacity_pools p ON c.capacity_pool_id=p.id AND c.recycler_org_id=p.recycler_org_id' 8
  check_scalar matcher_repair fk_checks 'SELECT @@foreign_key_checks' 1
  dump_rows matcher_repair "$EVIDENCE_DIR/before-repeat.sql" "${REPAIR_TABLES[@]}"
  lb matcher_repair repeat update --context-filter=seed
  check_scalar matcher_repair repair_recorded_once "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106'" 1
  dump_rows matcher_repair "$EVIDENCE_DIR/after-repeat.sql" "${REPAIR_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/before-repeat.sql" "$EVIDENCE_DIR/after-repeat.sql" > "$EVIDENCE_DIR/repeat.diff"
  # Isolated test only: emulate manual repair, or a crash after data commit but
  # before Liquibase recorded completion. Do not edit live changelog records.
  mysql_query matcher_repair -e "DELETE FROM DATABASECHANGELOG WHERE ID='EWCSB129-106'"
  lb matcher_repair adopt-repaired update --context-filter=seed
  check_scalar matcher_repair repaired_state_adopted "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-106' AND EXECTYPE='EXECUTED'" 1
  dump_rows matcher_repair "$EVIDENCE_DIR/after-adopt.sql" "${REPAIR_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/before-repeat.sql" "$EVIDENCE_DIR/after-adopt.sql" > "$EVIDENCE_DIR/adopt-repaired.diff"
  check_scalar matcher_repair helper_removed "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema='matcher_repair' AND routine_name='repair_ewcsb129_config_ids'" 0
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'matcher-repair\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

reject_collector_seed() {
  local name="$1"
  dump_rows collector_seed "$EVIDENCE_DIR/$name-before.sql" recycler_collector_scopes organisations DATABASECHANGELOG
  if lb collector_seed "$name" update --context-filter=seed; then
    echo "FAIL: collector seed unexpectedly succeeded: $name" >&2; return 1
  fi
  grep -q 'SQL Precondition failed' "$EVIDENCE_DIR/collector_seed-$name.log"
  dump_rows collector_seed "$EVIDENCE_DIR/$name-after.sql" recycler_collector_scopes organisations DATABASECHANGELOG
  diff -u "$EVIDENCE_DIR/$name-before.sql" "$EVIDENCE_DIR/$name-after.sql" > "$EVIDENCE_DIR/$name-preserved.diff"
  check_scalar collector_seed "$name-not-recorded" "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB4-107'" 0
}

run_collector_scope_seed() {
  EVIDENCE_DIR="$RUN_DIR/collector-scopes"
  CHANGELOG="changelog-before-recycler-config.yaml"
  mkdir -p "$EVIDENCE_DIR"
  printf '\nChecking collector scope seed, migration guards and SQL visibility.\n'
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  lb collector_seed baseline --changelog-file=changelog-before-collector-scopes.yaml update --context-filter=seed
  check_scalar collector_seed initially_empty 'SELECT COUNT(*) FROM recycler_collector_scopes' 0

  mysql_query collector_seed -e "UPDATE organisations SET status='DISABLED' WHERE organisation_id='COL-001'"
  reject_collector_seed inactive_collector
  mysql_query collector_seed -e "UPDATE organisations SET status='ACTIVE' WHERE organisation_id='COL-001'; UPDATE organisations SET status='DISABLED' WHERE organisation_id='COL-002'"
  reject_collector_seed inactive_backup_collector
  mysql_query collector_seed -e "UPDATE organisations SET status='ACTIVE' WHERE organisation_id='COL-002'"
  mysql_query collector_seed -e "UPDATE organisations SET status='ACTIVE' WHERE organisation_id='COL-001'; UPDATE organisations SET organisation_type='DONOR' WHERE organisation_id='PROC-002'"
  reject_collector_seed wrong_recycler_type
  mysql_query collector_seed -e "UPDATE organisations SET organisation_type='PROCESSING_FACILITY' WHERE organisation_id='PROC-002'"

  local scenario scope_id recycler zone active valid_from valid_until
  for scenario in inactive expired future invalid_id collision backup_collision; do
    scope_id=e1070000-0000-4000-8000-000000000001
    recycler=PROC-001; zone=NORTH; active=1; valid_from="'2020-01-01'"; valid_until=NULL
    case "$scenario" in
      inactive) active=0 ;;
      expired) valid_until="'2020-01-02'" ;;
      future) valid_from='UTC_TIMESTAMP(6)+INTERVAL 1 DAY' ;;
      invalid_id) scope_id=manual-scope ;;
      collision) recycler=PROC-002 ;;
      backup_collision) scope_id=e1070000-0000-4000-8000-000000000003 ;;
    esac
    mysql_query collector_seed -e "INSERT INTO recycler_collector_scopes VALUES('$scope_id','$recycler','COL-001','$zone',$active,7,$valid_from,$valid_until,'2020-01-01','2020-01-01')"
    reject_collector_seed "$scenario-scope"
    mysql_query collector_seed -e 'DELETE FROM recycler_collector_scopes'
  done

  # Adopt an existing usable identity and retain unrelated authorizations.
  mysql_query collector_seed <<'COLLECTOR_EXISTING'
INSERT INTO recycler_collector_scopes VALUES
('e1079999-0000-4000-8000-000000000001','PROC-001','COL-001','NORTH',1,9,'2020-01-01','2099-01-01','2020-01-01','2020-01-02'),
('e1079999-0000-4000-8000-000000000002','PROC-001','COL-002','SOUTH',1,3,'2020-01-01',NULL,'2020-01-01','2020-01-02');
COLLECTOR_EXISTING
  dump_rows collector_seed "$EVIDENCE_DIR/existing-before.sql" recycler_collector_scopes
  lb collector_seed upgrade update --context-filter=seed
  dump_rows collector_seed "$EVIDENCE_DIR/existing-after.sql" recycler_collector_scopes --where="id NOT IN ('e1070000-0000-4000-8000-000000000002','e1070000-0000-4000-8000-000000000003')"
  diff -u "$EVIDENCE_DIR/existing-before.sql" "$EVIDENCE_DIR/existing-after.sql" > "$EVIDENCE_DIR/existing-preserved.diff"
  check_scalar collector_seed confirmed_pairs "SELECT GROUP_CONCAT(CONCAT(recycler_org_id,':',zone) ORDER BY recycler_org_id) FROM recycler_collector_scopes WHERE collector_org_id='COL-001'" 'PROC-001:NORTH,PROC-002:EAST'
  check_scalar collector_seed backup_uses_existing_account "SELECT COUNT(*) FROM recycler_collector_scopes s JOIN users u ON u.organisation_id=s.collector_org_id WHERE s.id='e1070000-0000-4000-8000-000000000003' AND s.recycler_org_id='PROC-001' AND s.zone='NORTH' AND u.user_id='USR-006' AND u.role_code='COLLECTOR' AND u.status='ACTIVE'" 1
  check_scalar collector_seed recorded_once "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB4-107' AND EXECTYPE='EXECUTED'" 1
  dump_rows collector_seed "$EVIDENCE_DIR/before-repeat.sql" recycler_collector_scopes DATABASECHANGELOG
  lb collector_seed repeat update --context-filter=seed
  dump_rows collector_seed "$EVIDENCE_DIR/after-repeat.sql" recycler_collector_scopes DATABASECHANGELOG
  diff -u "$EVIDENCE_DIR/before-repeat.sql" "$EVIDENCE_DIR/after-repeat.sql" > "$EVIDENCE_DIR/repeat.diff"

  # SQL-only fixtures exercise the collector read query's current-claim/zone
  # predicate. They do not claim to execute API assignment or handoff commands.
  mysql_query collector_seed <<'COLLECTOR_VISIBILITY'
CREATE TEMPORARY TABLE scope_visibility_cases(n INT PRIMARY KEY, recycler VARCHAR(32), zone VARCHAR(16), actor VARCHAR(32));
INSERT INTO scope_visibility_cases VALUES
(1,'PROC-001','NORTH','USR-007'),(2,'PROC-002','NORTH','USR-008'),
(3,'PROC-001','EAST','USR-007'),(4,'PROC-002','EAST','USR-008');
INSERT INTO ewaste_batches(id,organization_id,created_by,category,quantity,estimated_weight_kg,condition_rating,zone,collection_deadline,created_at,updated_at)
SELECT CONCAT('e1071000-0000-4000-8000-00000000000',n),'DON-001','USR-003','ICT_EQUIPMENT',1,10,'FUNCTIONAL',zone,'2026-01-04','2026-01-01','2026-01-01' FROM scope_visibility_cases;
INSERT INTO batch_claims(id,batch_id,recycler_org_id,claimed_by,idempotency_key)
SELECT CONCAT('e1072000-0000-4000-8000-00000000000',n),CONCAT('e1071000-0000-4000-8000-00000000000',n),recycler,actor,CONCAT('scope-visibility-claim-00',n) FROM scope_visibility_cases;
UPDATE ewaste_batches b JOIN batch_claims c ON c.batch_id=b.id
SET b.status='APPROVED',b.submitted_at='2026-01-01',b.current_claim_id=c.id;
COLLECTOR_VISIBILITY
  local visibility="SELECT COALESCE(GROUP_CONCAT(RIGHT(b.id,1) ORDER BY b.id),'') FROM ewaste_batches b WHERE b.status='APPROVED' AND EXISTS (SELECT 1 FROM batch_claims c JOIN recycler_collector_scopes s ON s.recycler_org_id=c.recycler_org_id AND s.collector_org_id='COL-001' AND s.zone=b.zone AND s.is_active=TRUE AND s.valid_from<=UTC_TIMESTAMP(6) AND (s.valid_until IS NULL OR s.valid_until>UTC_TIMESTAMP(6)) WHERE c.id=b.current_claim_id AND c.batch_id=b.id AND c.claim_epoch=b.claim_epoch AND c.claim_status='ACCEPTED')"
  check_scalar collector_seed only_authorized_pairs_visible "$visibility" '1,4'
  check_scalar collector_seed backup_sees_only_north_proc001 "${visibility//COL-001/COL-002}" '1'
  check_scalar collector_seed inactive_scopes_hidden "START TRANSACTION; UPDATE recycler_collector_scopes SET is_active=0 WHERE collector_org_id='COL-001'; $visibility; ROLLBACK" ''
  check_scalar collector_seed non_approved_batches_hidden "START TRANSACTION; UPDATE ewaste_batches SET status='MATCHED',current_claim_id=NULL; $visibility; ROLLBACK" ''
  check_scalar collector_seed rejected_claims_hidden "START TRANSACTION; UPDATE batch_claims SET claim_status='REJECTED'; $visibility; ROLLBACK" ''
  # The same seeded NORTH scopes support two eligible collectors competing
  # for the one open assignment, then a different collector replacing rejection.
  local side actor org scope assignment
  printf 'case\tresult\twinner_exit\tloser_exit\tlock_wait_observed\trejected_key\n' > "$EVIDENCE_DIR/concurrency.tsv"
  mkdir -p "$WORK_DIR/races"
  for side in 1 2; do
    if [[ "$side" == 1 ]]; then actor=USR-005; org=COL-001; else actor=USR-006; org=COL-002; fi
    scope=$(mysql_query collector_seed --skip-column-names -e "SELECT id FROM recycler_collector_scopes WHERE recycler_org_id='PROC-001' AND collector_org_id='$org' AND zone='NORTH'")
    assignment="e1073000-0000-4000-8000-00000000000$side"
    cat > "$WORK_DIR/races/seeded-collector-$side.sql" <<SQL_SEEDED_COLLECTOR
INSERT INTO batch_assignments(id,batch_id,claim_id,recycler_org_id,collector_org_id,collector_user_id,collector_scope_id,assignment_sequence,claim_epoch,assignment_status,assigned_at,responded_at,version,created_at,updated_at)
VALUES('$assignment','e1071000-0000-4000-8000-000000000001','e1072000-0000-4000-8000-000000000001','PROC-001','$org','$actor','$scope',1,1,'ACCEPTED',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6));
UPDATE ewaste_batches SET status='ASSIGNED',current_assignment_id='$assignment',version=version+1 WHERE id='e1071000-0000-4000-8000-000000000001';
SQL_SEEDED_COLLECTOR
  done
  run_sql_race collector_seed seeded_collector_assignment \
    "SELECT id FROM ewaste_batches WHERE id='e1071000-0000-4000-8000-000000000001' FOR UPDATE" \
    "$WORK_DIR/races/seeded-collector-1.sql" "$WORK_DIR/races/seeded-collector-2.sql" 'uq_assignment_(open|sequence)'
  check_scalar collector_seed exactly_one_open_assignment 'SELECT COUNT(*) FROM batch_assignments WHERE active_batch_id IS NOT NULL' 1
  mysql_query collector_seed <<'SQL_SEEDED_REPLACEMENT'
START TRANSACTION;
UPDATE batch_assignments SET assignment_status='SUPERSEDED',closure_reason='REJECTED',rejection_reason='SQL test rejection',closed_at=UTC_TIMESTAMP(6),updated_at=UTC_TIMESTAMP(6),version=version+1
WHERE id='e1073000-0000-4000-8000-000000000001';
UPDATE ewaste_batches SET status='APPROVED',current_assignment_id=NULL,version=version+1 WHERE id='e1071000-0000-4000-8000-000000000001';
INSERT INTO batch_assignments(id,batch_id,claim_id,recycler_org_id,collector_org_id,collector_user_id,collector_scope_id,assignment_sequence,claim_epoch,previous_assignment_id,reassignment_reason,assignment_status,assigned_at,responded_at,version,created_at,updated_at)
VALUES('e1073000-0000-4000-8000-000000000002','e1071000-0000-4000-8000-000000000001','e1072000-0000-4000-8000-000000000001','PROC-001','COL-002','USR-006','e1070000-0000-4000-8000-000000000003',2,1,'e1073000-0000-4000-8000-000000000001','SQL test replacement','ACCEPTED',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6));
UPDATE ewaste_batches SET status='ASSIGNED',current_assignment_id='e1073000-0000-4000-8000-000000000002',version=version+1 WHERE id='e1071000-0000-4000-8000-000000000001';
COMMIT;
SQL_SEEDED_REPLACEMENT
  check_scalar collector_seed replacement_preserves_history "SELECT GROUP_CONCAT(CONCAT(collector_org_id,':',assignment_status) ORDER BY assignment_sequence) FROM batch_assignments" 'COL-001:SUPERSEDED,COL-002:ACCEPTED'
  check_scalar collector_seed replacement_retains_claim "SELECT COUNT(*) FROM ewaste_batches b JOIN batch_assignments a ON a.id=b.current_assignment_id AND a.claim_id=b.current_claim_id WHERE a.collector_user_id='USR-006' AND a.collector_scope_id='e1070000-0000-4000-8000-000000000003' AND a.previous_assignment_id='e1073000-0000-4000-8000-000000000001'" 1
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'collector-scopes\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

reject_recycler_seed() {
  local name="$1"
  dump_rows recycler_upgrade "$EVIDENCE_DIR/$name-before.sql" "${RECYCLER_TABLES[@]}" DATABASECHANGELOG
  if lb recycler_upgrade "$name" --changelog-file=seed/108-seed-recycler-matching-config.sql update --context-filter=seed; then
    echo "FAIL: recycler seed unexpectedly succeeded: $name" >&2; return 1
  fi
  grep -q 'SQL Precondition failed' "$EVIDENCE_DIR/recycler_upgrade-$name.log"
  dump_rows recycler_upgrade "$EVIDENCE_DIR/$name-after.sql" "${RECYCLER_TABLES[@]}" DATABASECHANGELOG
  diff -u "$EVIDENCE_DIR/$name-before.sql" "$EVIDENCE_DIR/$name-after.sql" > "$EVIDENCE_DIR/$name-preserved.diff"
  check_scalar recycler_upgrade "$name-not-recorded" "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-108'" 0
}

run_recycler_matching_seed() {
  EVIDENCE_DIR="$RUN_DIR/recycler-matching-config"
  CHANGELOG="changelog-master.yaml"
  RECYCLER_TABLES=(recycler_matching_profiles recycler_capacity_pools recycler_category_capabilities recycler_service_zones)
  mkdir -p "$EVIDENCE_DIR"
  printf '\nChecking recycler matching configuration on clean and partial databases.\n'
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  lb recycler_seed validate validate
  lb recycler_seed schema update
  check_scalar recycler_seed excluded_without_seed "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-108'" 0
  for table in "${RECYCLER_TABLES[@]}"; do
    check_scalar recycler_seed "${table}_empty_without_seed" "SELECT COUNT(*) FROM $table" 0
  done
  lb recycler_seed seed update --context-filter=seed
  check_scalar recycler_seed active_profiles 'SELECT COUNT(*) FROM recycler_matching_profiles WHERE is_active=1 AND version=1 AND created_at=updated_at' 2
  check_scalar recycler_seed main_capacity "SELECT COUNT(*) FROM recycler_capacity_pools WHERE pool_code='MAIN' AND total_kg=50000 AND reserved_kg=0 AND is_active=1 AND version=1" 2
  check_scalar recycler_seed category_settings "SELECT COUNT(*) FROM recycler_category_capabilities c JOIN recycler_capacity_pools p ON p.id=c.capacity_pool_id AND p.recycler_org_id=c.recycler_org_id WHERE p.pool_code='MAIN' AND c.is_active=1 AND c.version=1 AND JSON_LENGTH(c.accepted_conditions_json)=3 AND JSON_CONTAINS(c.accepted_conditions_json,'[\"FUNCTIONAL\",\"REPAIRABLE\",\"END_OF_LIFE\"]') AND c.supports_data_bearing=(c.category IN ('ICT_EQUIPMENT','CONSUMER_ELECTRONICS'))" 8
  check_scalar recycler_seed zone_settings 'SELECT COUNT(*) FROM recycler_service_zones WHERE is_active=1 AND minimum_lead_minutes=0 AND version=1' 10
  check_scalar recycler_seed valid_ids "SELECT COUNT(*) FROM (SELECT id FROM recycler_capacity_pools UNION ALL SELECT id FROM recycler_category_capabilities UNION ALL SELECT id FROM recycler_service_zones) s WHERE REGEXP_LIKE(id,'^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$','c')" 20
  check_scalar recycler_seed all_category_zone_pairs 'SELECT COUNT(*) FROM recycler_matching_profiles p JOIN recycler_category_capabilities c ON c.recycler_org_id=p.recycler_org_id JOIN recycler_service_zones z ON z.recycler_org_id=p.recycler_org_id' 40
  check_scalar recycler_seed no_business_rows 'SELECT (SELECT COUNT(*) FROM ewaste_batches)+(SELECT COUNT(*) FROM matching_decisions)+(SELECT COUNT(*) FROM matched_results)+(SELECT COUNT(*) FROM batch_claims)+(SELECT COUNT(*) FROM batch_assignments)+(SELECT COUNT(*) FROM batch_handoffs)+(SELECT COUNT(*) FROM event_outbox)+(SELECT COUNT(*) FROM batch_audit_events)+(SELECT COUNT(*) FROM command_idempotency)' 0
  dump_rows recycler_seed "$EVIDENCE_DIR/clean-before-repeat.sql" "${RECYCLER_TABLES[@]}" recycler_collector_scopes DATABASECHANGELOG
  lb recycler_seed repeat update --context-filter=seed
  dump_rows recycler_seed "$EVIDENCE_DIR/clean-after-repeat.sql" "${RECYCLER_TABLES[@]}" recycler_collector_scopes DATABASECHANGELOG
  diff -u "$EVIDENCE_DIR/clean-before-repeat.sql" "$EVIDENCE_DIR/clean-after-repeat.sql" > "$EVIDENCE_DIR/clean-repeat.diff"

  lb recycler_upgrade baseline --changelog-file=changelog-before-recycler-config.yaml update --context-filter=seed
  # Compatible manual configuration: custom IDs/versions, nonzero reservations,
  # reordered conditions and missing rows. All existing values must survive.
  mysql_query recycler_upgrade <<'RECYCLER_EXISTING'
INSERT INTO recycler_matching_profiles VALUES('PROC-001',1,9,'2026-01-01','2026-01-02');
INSERT INTO recycler_capacity_pools VALUES('f1080000-0000-4000-8000-000000000001','PROC-001','MAIN',50000,125,1,4,'2026-01-02');
INSERT INTO recycler_category_capabilities VALUES('f1080000-0000-4000-8000-000000000002','PROC-001','ICT_EQUIPMENT','["END_OF_LIFE","FUNCTIONAL","REPAIRABLE"]',1,1,'f1080000-0000-4000-8000-000000000001',2,'2026-01-02');
INSERT INTO recycler_service_zones VALUES('f1080000-0000-4000-8000-000000000003','PROC-001','NORTH',0,1,7,'2026-01-02');
RECYCLER_EXISTING
  local scenario change restore
  while IFS='|' read -r scenario change restore; do
    mysql_query recycler_upgrade -e "$change" </dev/null
    reject_recycler_seed "$scenario" </dev/null
    mysql_query recycler_upgrade -e "$restore" </dev/null
  done <<'RECYCLER_REJECTIONS'
inactive_org|UPDATE organisations SET status='DISABLED' WHERE organisation_id='PROC-002'|UPDATE organisations SET status='ACTIVE' WHERE organisation_id='PROC-002'
wrong_org_type|UPDATE organisations SET organisation_type='DONOR' WHERE organisation_id='PROC-002'|UPDATE organisations SET organisation_type='PROCESSING_FACILITY' WHERE organisation_id='PROC-002'
inactive_profile|UPDATE recycler_matching_profiles SET is_active=0|UPDATE recycler_matching_profiles SET is_active=1
capacity_conflict|UPDATE recycler_capacity_pools SET total_kg=1000|UPDATE recycler_capacity_pools SET total_kg=50000
inactive_pool|UPDATE recycler_capacity_pools SET is_active=0|UPDATE recycler_capacity_pools SET is_active=1
condition_conflict|UPDATE recycler_category_capabilities SET accepted_conditions_json='["FUNCTIONAL"]'|UPDATE recycler_category_capabilities SET accepted_conditions_json='["END_OF_LIFE","FUNCTIONAL","REPAIRABLE"]'
data_bearing_conflict|UPDATE recycler_category_capabilities SET supports_data_bearing=0|UPDATE recycler_category_capabilities SET supports_data_bearing=1
zone_conflict|UPDATE recycler_service_zones SET minimum_lead_minutes=60|UPDATE recycler_service_zones SET minimum_lead_minutes=0
invalid_id|UPDATE recycler_service_zones SET id='manual-zone'|UPDATE recycler_service_zones SET id='f1080000-0000-4000-8000-000000000003'
pool_id_collision|INSERT INTO recycler_capacity_pools VALUES('b1080000-0000-4000-8000-000000000002','PROC-001','OTHER',1,0,1,1,UTC_TIMESTAMP(6))|DELETE FROM recycler_capacity_pools WHERE pool_code='OTHER'
category_id_collision|UPDATE recycler_category_capabilities SET id='c1080000-0000-4000-8000-000000000005'|UPDATE recycler_category_capabilities SET id='f1080000-0000-4000-8000-000000000002'
zone_id_collision|UPDATE recycler_service_zones SET id='d1080000-0000-4000-8000-000000000006'|UPDATE recycler_service_zones SET id='f1080000-0000-4000-8000-000000000003'
RECYCLER_REJECTIONS
  dump_rows recycler_upgrade "$EVIDENCE_DIR/profile-before.sql" recycler_matching_profiles
  dump_rows recycler_upgrade "$EVIDENCE_DIR/existing-before.sql" "${RECYCLER_TABLES[@]:1}"
  dump_rows recycler_upgrade "$EVIDENCE_DIR/scopes-before.sql" recycler_collector_scopes
  lb recycler_upgrade upgrade --changelog-file=seed/108-seed-recycler-matching-config.sql update --context-filter=seed
  check_scalar recycler_upgrade filled_counts 'SELECT CONCAT((SELECT COUNT(*) FROM recycler_matching_profiles),":",(SELECT COUNT(*) FROM recycler_capacity_pools),":",(SELECT COUNT(*) FROM recycler_category_capabilities),":",(SELECT COUNT(*) FROM recycler_service_zones))' '2:2:8:10'
  check_scalar recycler_upgrade shared_existing_pool "SELECT COUNT(*) FROM recycler_category_capabilities WHERE recycler_org_id='PROC-001' AND capacity_pool_id='f1080000-0000-4000-8000-000000000001'" 4
  check_scalar recycler_upgrade reservation_preserved "SELECT total_kg-reserved_kg FROM recycler_capacity_pools WHERE recycler_org_id='PROC-001' AND pool_code='MAIN'" '49875.00'
  dump_rows recycler_upgrade "$EVIDENCE_DIR/profile-after.sql" recycler_matching_profiles --where="recycler_org_id='PROC-001'"
  dump_rows recycler_upgrade "$EVIDENCE_DIR/existing-after.sql" "${RECYCLER_TABLES[@]:1}" --where="id LIKE 'f108%'"
  dump_rows recycler_upgrade "$EVIDENCE_DIR/scopes-after.sql" recycler_collector_scopes
  for scenario in profile existing scopes; do
    diff -u "$EVIDENCE_DIR/$scenario-before.sql" "$EVIDENCE_DIR/$scenario-after.sql" > "$EVIDENCE_DIR/$scenario-preserved.diff"
  done
  # Simulate data committed before changelog recording in this disposable DB.
  mysql_query recycler_upgrade -e "DELETE FROM DATABASECHANGELOG WHERE ID='EWCSB129-108'"
  dump_rows recycler_upgrade "$EVIDENCE_DIR/adopt-before.sql" "${RECYCLER_TABLES[@]}"
  lb recycler_upgrade adopt --changelog-file=seed/108-seed-recycler-matching-config.sql update --context-filter=seed
  dump_rows recycler_upgrade "$EVIDENCE_DIR/adopt-after.sql" "${RECYCLER_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/adopt-before.sql" "$EVIDENCE_DIR/adopt-after.sql" > "$EVIDENCE_DIR/adopt.diff"
  check_scalar recycler_upgrade recorded_once "SELECT COUNT(*) FROM DATABASECHANGELOG WHERE ID='EWCSB129-108' AND EXECTYPE='EXECUTED'" 1
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'recycler-matching-config\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

run_processing() {
  EVIDENCE_DIR="$RUN_DIR/processing"
  CHANGELOG="changelog-processing-schema.yaml"
  mkdir -p "$EVIDENCE_DIR"
  printf 'test_name\tactual\texpected\n' > "$EVIDENCE_DIR/migration-checks.tsv"
  # Exclude all seeds: upgrading the schema must not modify existing configuration.
  awk '
    NR == 1 { print; next }
    /^  - include:/ { block=$0 ORS; next }
    { block=block $0 ORS }
    /relativeToChangelogFile:/ { if (block ~ /file: changes\//) printf "%s",block; block="" }
  ' database/changelog-master.yaml > "database/$CHANGELOG"
  lb processing_clean validate validate
  lb processing_clean clean update --context-filter=none
  for seed in database/seed/10[123]-*.sql; do
    lb processing_clean "$(basename "$seed" .sql)" --changelog-file="${seed#database/}" update --context-filter=seed
  done
  # c4_clean has committed claims, assignments, handoffs, audit and concurrent-race history.
  LEGACY_TABLES=(organisations roles users sessions login_audit ewaste_batches command_idempotency batch_audit_events event_outbox matching_rule_sets recycler_matching_profiles recycler_capacity_pools recycler_category_capabilities recycler_service_zones matching_decisions matched_results batch_claims capacity_reservations recycler_collector_scopes batch_assignments batch_handoffs assignment_actions)
  dump_rows c4_clean "$EVIDENCE_DIR/legacy-before.sql" "${LEGACY_TABLES[@]}"
  mysql_query c4_clean -e 'SELECT ID,AUTHOR,FILENAME,MD5SUM,DATEEXECUTED FROM DATABASECHANGELOG ORDER BY ID' > "$EVIDENCE_DIR/changelog-before.tsv"
  lb c4_clean upgrade-preview update-sql --context-filter=none
  lb c4_clean upgrade update --context-filter=none
  dump_rows c4_clean "$EVIDENCE_DIR/legacy-after.sql" "${LEGACY_TABLES[@]}"
  diff -u "$EVIDENCE_DIR/legacy-before.sql" "$EVIDENCE_DIR/legacy-after.sql" > "$EVIDENCE_DIR/legacy-preserved.diff"
  mysql_query c4_clean -e "SELECT ID,AUTHOR,FILENAME,MD5SUM,DATEEXECUTED FROM DATABASECHANGELOG WHERE ID NOT IN ('EWCSB3-026','EWCSB3-027','EWCSB3-028','EWCSB3-029','EWCSB3-030','EWSS22-031') ORDER BY ID" > "$EVIDENCE_DIR/changelog-after.tsv"
  diff -u "$EVIDENCE_DIR/changelog-before.tsv" "$EVIDENCE_DIR/changelog-after.tsv" > "$EVIDENCE_DIR/changelog-preserved.diff"
  for db in processing_clean c4_clean; do
    mysql_query "$db" < database/tests/verify-processing-migrations.sql > "$EVIDENCE_DIR/$db-schema.tsv"
    if grep -q $'\tFAIL' "$EVIDENCE_DIR/$db-schema.tsv"; then cat "$EVIDENCE_DIR/$db-schema.tsv" >&2; return 1; fi
    for pass in first repeat; do
      mysql_query "$db" < database/tests/test-processing-fixtures.sql > "$EVIDENCE_DIR/$db-$pass-fixtures.tsv" 2> "$EVIDENCE_DIR/$db-$pass-errors.txt" || {
        cat "$EVIDENCE_DIR/$db-$pass-errors.txt" >&2; return 1;
      }
      if grep -q $'\tFAIL\t' "$EVIDENCE_DIR/$db-$pass-fixtures.tsv"; then cat "$EVIDENCE_DIR/$db-$pass-fixtures.tsv" >&2; return 1; fi
    done
    diff -u "$EVIDENCE_DIR/$db-first-fixtures.tsv" "$EVIDENCE_DIR/$db-repeat-fixtures.tsv" > "$EVIDENCE_DIR/$db-fixtures-repeat.diff"
    TABLES=("${LEGACY_TABLES[@]}" batch_evidence batch_receipts batch_treatments batch_impact_metrics batch_anomalies DATABASECHANGELOG)
    dump_rows "$db" "$EVIDENCE_DIR/$db-before-repeat.sql" "${TABLES[@]}"
    lb "$db" repeat update --context-filter=none
    dump_rows "$db" "$EVIDENCE_DIR/$db-after-repeat.sql" "${TABLES[@]}"
    diff -u "$EVIDENCE_DIR/$db-before-repeat.sql" "$EVIDENCE_DIR/$db-after-repeat.sql" > "$EVIDENCE_DIR/$db-repeat.diff"
    check_scalar "$db" no_fixture_residue "SELECT COUNT(*) FROM ewaste_batches WHERE id LIKE 'b3100000-%'" 0
  done
  printf 'PASS\n' > "$EVIDENCE_DIR/result.txt"
  printf 'processing\tPASS\t%s\n' "$EVIDENCE_DIR" >> "$RUN_DIR/summary.tsv"
}

main() {
  write_embedded_tests
  cd "$WORK_DIR"
  # Prior release for upgrade tests; retain canonical relative include paths.
  awk '
    NR == 1 { print; next }
    /^  - include:/ { block = $0 ORS; next }
    { block = block $0 ORS }
    /relativeToChangelogFile:/ {
      if (block ~ /(changes\/0(0[1-9]|1[0-9]|2[0-5])|seed\/10[1-7])/) printf "%s", block
      block = ""
    }
  ' database/changelog-master.yaml > database/changelog-before-recycler-config.yaml
  awk '
    NR == 1 { print; next }
    /^  - include:/ { block = $0 ORS; next }
    { block = block $0 ORS }
    /relativeToChangelogFile:/ {
      if (block !~ /107-seed-recycler-collector-scopes/) printf "%s", block
      block = ""
    }
  ' database/changelog-before-recycler-config.yaml > database/changelog-before-collector-scopes.yaml
  "${HASH[@]}" -c approved-migrations.sha256 > "$RUN_DIR/approved-migrations.txt"
  printf 'suite\tresult\tevidence_directory\n' > "$RUN_DIR/summary.tsv"
  echo 'Starting one isolated MySQL instance for all schema boundaries.'
  "${COMPOSE[@]}" build liquibase > "$RUN_DIR/build.log" 2>&1 || { cat "$RUN_DIR/build.log" >&2; return 1; }
  DOCKER_STARTED=1
  "${COMPOSE[@]}" up -d --wait --wait-timeout 180 mysql > "$RUN_DIR/startup.log" 2>&1 || { cat "$RUN_DIR/startup.log" >&2; return 1; }
  "${COMPOSE[@]}" exec -T -e MYSQL_PWD=persistence-root-test-only mysql mysql -uroot <<'PERSISTENCE_DATABASES'
CREATE DATABASE processing_clean CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
GRANT ALL ON processing_clean.* TO 'persistence_test'@'%';
CREATE DATABASE c1_upgrade CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE c3_clean CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE c3_upgrade CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE c4_clean CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE c4_upgrade CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE collector_seed CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE matcher_repair CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE recycler_seed CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE recycler_upgrade CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
GRANT ALL ON c1_upgrade.* TO 'persistence_test'@'%';
GRANT ALL ON c3_clean.* TO 'persistence_test'@'%';
GRANT ALL ON c3_upgrade.* TO 'persistence_test'@'%';
GRANT ALL ON c4_clean.* TO 'persistence_test'@'%';
GRANT ALL ON c4_upgrade.* TO 'persistence_test'@'%';
GRANT ALL ON collector_seed.* TO 'persistence_test'@'%';
GRANT ALL ON matcher_repair.* TO 'persistence_test'@'%';
GRANT ALL ON recycler_seed.* TO 'persistence_test'@'%';
GRANT ALL ON recycler_upgrade.* TO 'persistence_test'@'%';
GRANT SELECT ON performance_schema.data_lock_waits TO 'persistence_test'@'%';
PERSISTENCE_DATABASES
  mysql_query c1_clean -e 'SELECT VERSION() AS mysql_version, @@sql_mode AS sql_mode, @@global.time_zone AS global_time_zone;' > "$RUN_DIR/runtime.tsv"
  "${COMPOSE[@]}" run --rm -T liquibase --version > "$RUN_DIR/liquibase-version.txt" 2>&1
  run_c1
  run_c3
  run_c4
  run_matcher_repair
  run_collector_scope_seed
  run_recycler_matching_seed
  run_processing
  cat "$RUN_DIR/summary.tsv"
  echo 'All embedded schema, migration and SQL persistence checks passed.'
}
main "$@"
