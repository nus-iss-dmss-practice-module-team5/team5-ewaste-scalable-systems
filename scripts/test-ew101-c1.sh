#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/database/docker-compose.persistence.yml"
VERIFY_SQL="$ROOT_DIR/database/tests/verify-ew101-c1.sql"

DB_NAME="${EWASTE_DB_NAME:-ewaste}"
DB_USER="${EWASTE_DB_USER:-ewaste_app}"
DB_PASSWORD="${EWASTE_DB_PASSWORD:-ewaste-local-only}"
DB_ROOT_PASSWORD="${EWASTE_DB_ROOT_PASSWORD:-root-local-only}"
KEEP_DB="${KEEP_DB:-0}"

cleanup() {
  if [[ "$KEEP_DB" != "1" ]]; then
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: Docker is required." >&2
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "ERROR: Docker Compose v2 is required." >&2
  exit 1
fi

echo "[1/6] Starting a clean MySQL test database..."
docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
docker compose -f "$COMPOSE_FILE" up -d --build mysql

echo "[2/6] Waiting for MySQL health check..."
for attempt in $(seq 1 60); do
  if docker compose -f "$COMPOSE_FILE" exec -T -e MYSQL_PWD="$DB_ROOT_PASSWORD" mysql \
      mysqladmin ping -h 127.0.0.1 -uroot --silent \
      >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    echo "ERROR: MySQL did not become healthy." >&2
    docker compose -f "$COMPOSE_FILE" logs mysql >&2
    exit 1
  fi
  sleep 2
done

echo "[3/6] Validating the Liquibase changelog..."
docker compose -f "$COMPOSE_FILE" run --rm liquibase validate

echo "[4/6] Applying schema and explicit synthetic seed context..."
docker compose -f "$COMPOSE_FILE" run --rm liquibase update --context-filter=seed

echo "[5/6] Re-running update to prove migration idempotency..."
docker compose -f "$COMPOSE_FILE" run --rm liquibase update --context-filter=seed

echo "[6/6] Running database assertions..."
results="$(
  docker compose -f "$COMPOSE_FILE" exec -T -e MYSQL_PWD="$DB_PASSWORD" mysql \
    mysql --batch --raw \
      -u"$DB_USER" \
      "$DB_NAME" < "$VERIFY_SQL"
)"

printf '%s\n' "$results"

if printf '%s\n' "$results" | awk -F '\t' 'NR > 1 && $2 != "PASS" { failed = 1 } END { exit failed }'; then
  echo "EW-101-C1 JWT persistence and login-audit verification PASSED."
else
  echo "EW-101-C1 JWT persistence and login-audit verification FAILED." >&2
  exit 1
fi

if [[ "$KEEP_DB" == "1" ]]; then
  echo "KEEP_DB=1: MySQL remains available on localhost:${EWASTE_DB_PORT:-3307}."
fi
