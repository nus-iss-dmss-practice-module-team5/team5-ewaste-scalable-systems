#!/usr/bin/env bash
# Matching/processing analytics verification: Python, Go, real MySQL and Kafka.
# Every database/broker lives in an isolated disposable Compose project.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
PROJECT="matcher-test-$(date -u +%Y%m%dt%H%M%Sz)-$$"
mkdir -p artifacts/matcher
RUN="$(mktemp -d "$ROOT/artifacts/matcher/$(date -u +%Y%m%dt%H%M%Sz)-XXXXXX")"
COMPOSE=(docker compose -p "$PROJECT" -f src/matcher/testing/compose.yml)
finish() {
  local code=$?
  trap - EXIT
  "${COMPOSE[@]}" logs --no-color > "$RUN/services.log" 2>&1 || true
  "${COMPOSE[@]}" down --volumes > "$RUN/cleanup.log" 2>&1 || code=1
  if [[ "$code" == 0 ]]; then echo PASS > "$RUN/result.txt"; else echo FAIL > "$RUN/result.txt"; fi
  printf 'Matcher evidence: %s\n' "$RUN"
  exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git rev-parse HEAD > "$RUN/base-commit.txt"
if command -v sha256sum >/dev/null; then HASH=(sha256sum); else HASH=(shasum -a 256); fi
tr -d '\r' < contracts/matching/approved-contracts.sha256 | "${HASH[@]}" -c - > "$RUN/contracts.txt"
# Capture every source/configuration input, including the prerequisite backend.
while IFS= read -r file; do "${HASH[@]}" "$file"; done < <(
  git ls-files --cached --others --exclude-standard -- src/backend src/matcher contracts/matching database terraform/environments docs/analytics-worker.md scripts/test-matcher.sh scripts/deploy-matcher.sh .github/workflows/matcher.yml .github/workflows/cd-pipeline.yml .github/workflows/terraform-infra-create.yml .github/workflows/terraform-infra-destroy.yml | LC_ALL=C sort -u | while IFS= read -r file; do [[ ! -f "$file" ]] || printf '%s\n' "$file"; done
) > "$RUN/test-inputs.sha256"
for schema in src/backend/internal/matchingcontract/schemas/*.json; do
  name="${schema##*/}"
  if [[ -f "contracts/matching/contracts/$name" ]]; then original="contracts/matching/contracts/$name"; else original="contracts/matching/kafka/$name"; fi
  cmp "$schema" "$original"
done
bash -n scripts/test-matcher.sh scripts/deploy-matcher.sh

"${COMPOSE[@]}" build tests migrate api > "$RUN/build.log" 2>&1 || { cat "$RUN/build.log"; exit 1; }
"${COMPOSE[@]}" up -d --wait kafka mysql redis > "$RUN/start.log" 2>&1 || { cat "$RUN/start.log"; exit 1; }
"${COMPOSE[@]}" run --rm -T migrate > "$RUN/migrations.log" 2>&1 || { cat "$RUN/migrations.log"; exit 1; }
"${COMPOSE[@]}" run --rm -T migrate > "$RUN/migrations-repeat.log" 2>&1 || { cat "$RUN/migrations-repeat.log"; exit 1; }
"${COMPOSE[@]}" run --rm -T go-tests 2>&1 | tee "$RUN/go-tests.log"
"${COMPOSE[@]}" up -d api > "$RUN/api-start.log" 2>&1 || { cat "$RUN/api-start.log"; exit 1; }
"${COMPOSE[@]}" run --rm -T --user "$(id -u):$(id -g)" -v "$RUN:/evidence" \
  -e COVERAGE_FILE=/evidence/.coverage -e ANALYTICS_TEST_EVIDENCE=/evidence/analytics \
  tests python -m pytest -v -p no:cacheprovider src/matcher/tests --cov=matcher \
  --cov-fail-under=85 --cov-report=term-missing --cov-report=xml:/evidence/coverage.xml \
  --junitxml=/evidence/junit.xml 2>&1 | tee "$RUN/tests.log"
"${COMPOSE[@]}" exec -T mysql mysql -umatcher -pmatcher-local-only matcher_test -e \
  "SELECT COUNT(*) AS applied_changesets FROM DATABASECHANGELOG; SELECT outcome,COUNT(*) AS decisions FROM matching_decisions GROUP BY outcome; SELECT COUNT(*) AS candidate_rows FROM matched_results; SELECT event_type,publish_state,COUNT(*) AS events FROM event_outbox GROUP BY event_type,publish_state;" \
  > "$RUN/persistence.txt"
