#!/usr/bin/env bash
# Two real worker applications, two replicas each, real Kafka/Go/MySQL replay.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
PROJECT="workers-test-$(date -u +%Y%m%dt%H%M%Sz)-$$"
mkdir -p artifacts/worker-services
RUN="$(mktemp -d "$ROOT/artifacts/worker-services/$(date -u +%Y%m%dt%H%M%Sz)-XXXXXX")"
COMPOSE=(docker compose -p "$PROJECT" -f src/matcher/testing/compose.yml -f src/matcher/testing/compose.workers.yml)
finish() {
  local code=$?
  trap - EXIT
  "${COMPOSE[@]}" ps --format json > "$RUN/apps.json" 2>&1 || true
  "${COMPOSE[@]}" images --format json > "$RUN/images.json" 2>&1 || true
  "${COMPOSE[@]}" logs --no-color > "$RUN/services.log" 2>&1 || true
  if [[ "$code" == 0 && "${KEEP_WORKER_APPS:-0}" == 1 ]]; then
    printf 'Running Compose project: %s\n' "$PROJECT"
    printf '%q ' "${COMPOSE[@]}" down --volumes > "$RUN/cleanup-command.txt"
    printf '\n' >> "$RUN/cleanup-command.txt"
  else
    "${COMPOSE[@]}" down --volumes > "$RUN/cleanup.log" 2>&1 || code=1
  fi
  if [[ "$code" == 0 ]]; then echo PASS > "$RUN/result.txt"; else echo FAIL > "$RUN/result.txt"; fi
  printf 'Worker service evidence: %s\n' "$RUN"
  exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git rev-parse HEAD > "$RUN/base-commit.txt"
git diff --binary > "$RUN/changes.patch"
if command -v sha256sum >/dev/null; then HASH=(sha256sum); else HASH=(shasum -a 256); fi
while IFS= read -r file; do "${HASH[@]}" "$file"; done < <(
  git ls-files --cached --others --exclude-standard -- src/backend src/matcher database contracts/matching \
    scripts/test-worker-services.sh | LC_ALL=C sort -u
) > "$RUN/test-inputs.sha256"
"${COMPOSE[@]}" build tests migrate api matching-worker analytics-worker > "$RUN/build.log" 2>&1 || { cat "$RUN/build.log"; exit 1; }
"${COMPOSE[@]}" up -d --wait kafka mysql redis > "$RUN/start.log" 2>&1
"${COMPOSE[@]}" run --rm -T migrate > "$RUN/migrations.log" 2>&1
# Three partitions allow both replicas in each group to own work.
"${COMPOSE[@]}" exec -T kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 \
  --create --topic ewaste.batch.events --partitions 3 --replication-factor 1 > "$RUN/topics.log" 2>&1
"${COMPOSE[@]}" exec -T kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 \
  --create --topic ewaste.batch.events.matching.dlq.v1 --partitions 1 --replication-factor 1 >> "$RUN/topics.log" 2>&1
"${COMPOSE[@]}" up -d --wait --scale matching-worker=2 --scale analytics-worker=2 \
  api matching-worker analytics-worker >> "$RUN/start.log" 2>&1
test_phase() {
  "${COMPOSE[@]}" run --rm -T --user "$(id -u):$(id -g)" -v "$RUN:/evidence" \
    -e WORKER_SERVICES_TEST=1 -e WORKER_SERVICES_PHASE="$1" -e ANALYTICS_TEST_EVIDENCE=/evidence \
    tests python -m pytest -v -p no:cacheprovider src/matcher/tests/test_worker_service_replay.py \
    --junitxml="/evidence/$1-junit.xml" 2>&1 | tee "$RUN/$1-tests.log"
}
test_phase initial
# Terminate one replica of each app without graceful offset commits. The remaining
# replicas must take all partitions before replacement replicas rejoin.
for service in matching-worker analytics-worker; do
  replica="$("${COMPOSE[@]}" ps -q "$service" | head -n 1)"
  docker kill "$replica" > /dev/null
done
test_phase takeover
"${COMPOSE[@]}" up -d --wait --scale matching-worker=2 --scale analytics-worker=2 \
  matching-worker analytics-worker >> "$RUN/start.log" 2>&1
test_phase replay
