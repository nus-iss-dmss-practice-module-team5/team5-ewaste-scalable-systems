#!/usr/bin/env bash
# Called by CD after the compatible API image is deployed.
# Approved migrations/configuration must already exist in the target database.
set -Eeuo pipefail
: "${TARGET_ENV:?dev, stg or prod is required}"
: "${MATCHER_IMAGE:?an immutable matcher image digest is required}"
: "${MATCHER_SIGNING_SECRET:?configure the dedicated matcher secret for this environment}"
[[ "$TARGET_ENV" =~ ^(dev|stg|prod)$ ]] || exit 2
[[ ${#MATCHER_SIGNING_SECRET} -ge 32 ]] || { echo 'Matcher signing secret must contain at least 32 characters.' >&2; exit 2; }
: "${ANALYTICS_SERVICE_TOKEN:?configure the dedicated analytics service token for this environment}"
[[ ${#ANALYTICS_SERVICE_TOKEN} -ge 32 ]] || { echo 'Analytics service token must contain at least 32 characters.' >&2; exit 2; }
[[ "$ANALYTICS_SERVICE_TOKEN" != "$MATCHER_SIGNING_SECRET" ]] || { echo 'Use separate analytics bearer and matcher signing secrets.' >&2; exit 2; }
RG="${RG:-rg-ewastescalable-${TARGET_ENV}}"
NAMESPACE="evh-ewastescalable-${TARGET_ENV}"
API="aca-ewastescalable-${TARGET_ENV}-api"
APP="aca-ewastescalable-${TARGET_ENV}-analytic"
ISSUER="ewaste-matching-${TARGET_ENV}"
AUDIENCE="ewaste-matching-api-${TARGET_ENV}"

# The existing IaC workflow is independent of CD. Wait for its exact resources;
# do not select the first namespace or silently continue with empty credentials.
ready=false
for attempt in {1..60}; do
  if az eventhubs namespace authorization-rule show --resource-group "$RG" --namespace-name "$NAMESPACE" --name auth-ewastescalable-workload --output none 2>/dev/null; then
    ready=true
    for topic in ewaste.batch.events ewaste.claim.events batch.collector.assigned batch.collection.completed batch.collection.failed ewaste.batch.events.matching.dlq.v1; do
      if ! az eventhubs eventhub show --resource-group "$RG" --namespace-name "$NAMESPACE" --name "$topic" --output none 2>/dev/null; then ready=false; break; fi
    done
    [[ "$ready" == true ]] && break
  fi
  sleep 10
done
[[ "$ready" == true ]] || { echo "Event Hubs prerequisites are missing; finish the IaC workflow first." >&2; exit 1; }
CONNECTION=$(az eventhubs namespace authorization-rule keys list --resource-group "$RG" --namespace-name "$NAMESPACE" --name auth-ewastescalable-workload --query primaryConnectionString -o tsv)
[[ -n "$CONNECTION" ]] || { echo 'Event Hubs connection string is empty.' >&2; exit 1; }
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then printf '::add-mask::%s\n' "$CONNECTION"; fi
API_FQDN=$(az containerapp show --name "$API" --resource-group "$RG" --query properties.configuration.ingress.fqdn -o tsv)
[[ -n "$API_FQDN" ]] || { echo 'API prerequisite is missing.' >&2; exit 1; }
# Infrastructure owns creation, identity, registry, scaling and ingress policy.
az containerapp show --name "$APP" --resource-group "$RG" --output none

az containerapp secret set --name "$API" --resource-group "$RG" --secrets kafka-conn="$CONNECTION" matching-signing-key="$MATCHER_SIGNING_SECRET" analytics-service-token="$ANALYTICS_SERVICE_TOKEN" --output none
az containerapp update --name "$API" --resource-group "$RG" --set-env-vars \
  EWASTE_MATCHING_ENABLED=true EWASTE_MATCHING_ISSUER="$ISSUER" EWASTE_MATCHING_AUDIENCE="$AUDIENCE" \
  EWASTE_MATCHING_SIGNING_SECRET=secretref:matching-signing-key \
  EWASTE_AUTH_ANALYTICS_SERVICE_TOKEN=secretref:analytics-service-token \
  EWASTE_KAFKA_ENABLED=true EWASTE_KAFKA_BROKERS="${NAMESPACE}.servicebus.windows.net:9093" \
  EWASTE_KAFKA_TLS_ENABLED=true EWASTE_KAFKA_SASL_MECHANISM=PLAIN \
  EWASTE_KAFKA_SASL_USERNAME='$ConnectionString' KAFKA_CONNECTION_STRING=secretref:kafka-conn \
  EWASTE_KAFKA_PUBLISH_TIMEOUT=60s EWASTE_KAFKA_BATCH_SIZE=1 EWASTE_KAFKA_LEASE_DURATION=180s \
  EWASTE_KAFKA_LEADER_LEASE_DURATION=300s --output none
API_REVISION=$(az containerapp show --name "$API" --resource-group "$RG" --query properties.latestRevisionName -o tsv)
az containerapp revision restart --name "$API" --resource-group "$RG" --revision "$API_REVISION" --output none

# Read-only authentication/API check. No synthetic business event or row is created.
MATCHER_TOKEN_ISSUER="$ISSUER" MATCHER_TOKEN_AUDIENCE="$AUDIENCE" MATCHER_API_FQDN="$API_FQDN" python3 - <<'PYPROBE'
import base64, hashlib, hmac, json, os, time, urllib.error, urllib.request, uuid
encode = lambda value: base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).rstrip(b"=")
for attempt in range(30):
    now = int(time.time())
    parts = [encode({"alg": "HS256", "typ": "JWT", "kid": "worker"}), encode({
        "iss": os.environ["MATCHER_TOKEN_ISSUER"], "aud": os.environ["MATCHER_TOKEN_AUDIENCE"],
        "sub": "matching-worker", "scope": "matching.read", "iat": now, "exp": now + 300})]
    unsigned = b".".join(parts)
    signature = base64.urlsafe_b64encode(hmac.new(os.environ["MATCHER_SIGNING_SECRET"].encode(), unsigned, hashlib.sha256).digest()).rstrip(b"=")
    token = (unsigned + b"." + signature).decode()
    req = urllib.request.Request("https://" + os.environ["MATCHER_API_FQDN"] + "/internal/v1/matching/runs/" + str(uuid.uuid4()), headers={"Authorization": "Bearer " + token})
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            pass
    except urllib.error.HTTPError as error:
        try:
            body = json.loads(error.read(8192))
            if error.code == 404 and body.get("code") == "NOT_FOUND" and body.get("transport_correlation_id"):
                print("Authenticated matching facade and durable command lookup are ready.")
                break
        except (ValueError, AttributeError):
            pass
    except OSError:
        pass
    time.sleep(10)
else:
    raise SystemExit("Matching facade is unavailable or incompatible; check API image, migrations and workload credentials.")
PYPROBE

ANALYTICS_FACADE_URL="https://${API_FQDN}" python3 - <<'PYANALYTICS'
import json, os, time, urllib.error, urllib.request, uuid
url = (os.environ["ANALYTICS_FACADE_URL"] + "/api/v1/batches/" + str(uuid.uuid4())
       + "/analytics-input?source_event_id=" + str(uuid.uuid4()))
for attempt in range(30):
    request = urllib.request.Request(url, headers={"Authorization": "Bearer " + os.environ["ANALYTICS_SERVICE_TOKEN"]})
    try:
        with urllib.request.urlopen(request, timeout=10):
            pass
    except urllib.error.HTTPError as error:
        try:
            if (error.code == 404 and error.headers.get("X-Analytics-Contract") == "processing-v1"
                    and json.loads(error.read(8192)).get("code") == "NOT_FOUND"):
                print("Authenticated analytics preparation API is ready.")
                break
        except (ValueError, AttributeError):
            pass
    except OSError:
        pass
    time.sleep(10)
else:
    raise SystemExit("Analytics preparation API is unavailable; check the API image, migrations and service token.")
PYANALYTICS

ENVIRONMENT=(
  KAFKA_BOOTSTRAP_SERVERS="${NAMESPACE}.servicebus.windows.net:9093"
  KAFKA_CONNECTION_STRING=secretref:kafka-conn
  MATCHER_TOPIC=ewaste.batch.events MATCHER_DLQ_TOPIC=ewaste.batch.events.matching.dlq.v1
  # New group avoids inheriting offsets acknowledged by the old analytics demo.
  MATCHER_GROUP_ID=matching-worker-v1 MATCHER_OFFSET_RESET=earliest
  ANALYTICS_ENABLED=true ANALYTICS_GROUP_ID=analytics-processing-v1
  MATCHER_FACADE_URL="https://${API_FQDN}" MATCHER_LOCAL_TEST=0
  ANALYTICS_FACADE_URL="https://${API_FQDN}" ANALYTICS_SERVICE_TOKEN=secretref:analytics-service-token
  MATCHER_SIGNING_SECRET=secretref:matching-signing-key MATCHER_TOKEN_ISSUER="$ISSUER" MATCHER_TOKEN_AUDIENCE="$AUDIENCE"
  MATCHER_MAX_POLL_MS=300000 MATCHER_SESSION_TIMEOUT_MS=30000 MATCHER_WORKERS=1
  MATCHER_HTTP_TIMEOUT_SECONDS=30 MATCHER_MAX_RESPONSE_BYTES=16777216 MATCHER_MAX_RECORD_BYTES=1000000
  MATCHER_DELIVERY_TIMEOUT_SECONDS=120 MATCHER_RETRY_BASE_SECONDS=1 MATCHER_RETRY_MAX_SECONDS=30
  MATCHER_MAX_REFRESHES=5 MATCHER_HEALTH_PORT=8000
)
az containerapp secret set --name "$APP" --resource-group "$RG" --secrets kafka-conn="$CONNECTION" matching-signing-key="$MATCHER_SIGNING_SECRET" analytics-service-token="$ANALYTICS_SERVICE_TOKEN" --output none
az containerapp update --name "$APP" --resource-group "$RG" --image "$MATCHER_IMAGE" --set-env-vars "${ENVIRONMENT[@]}" --output none
# A secret-only redeployment must reload the current secret as well.
REVISION=$(az containerapp show --name "$APP" --resource-group "$RG" --query properties.latestRevisionName -o tsv)
[[ -n "$REVISION" ]] || { echo 'Matcher revision was not assigned.' >&2; exit 1; }
az containerapp revision restart --name "$APP" --resource-group "$RG" --revision "$REVISION" --output none
FQDN=$(az containerapp show --name "$APP" --resource-group "$RG" --query properties.configuration.ingress.fqdn -o tsv)
[[ -n "$FQDN" ]] || { echo 'Matcher hostname was not assigned.' >&2; exit 1; }
ready=false
for attempt in {1..30}; do
  if [[ "$TARGET_ENV" == dev ]]; then
    if curl --silent --show-error --fail --max-time 10 "https://${FQDN}/readyz" >/dev/null; then ready=true; break; fi
  else
    # Stg/prod ingress stays private. Verify this deployment's exact revision,
    # not an older active revision. This is platform health, not a broker probe.
    state=$(az containerapp revision show --name "$APP" --resource-group "$RG" --revision "$REVISION" \
      --query "properties.runningState == 'Running' && properties.healthState == 'Healthy'" -o tsv)
    if [[ "$state" == true ]]; then ready=true; break; fi
  fi
  sleep 10
done
[[ "$ready" == true ]] || { echo 'Matcher did not become ready; inspect API/worker logs.' >&2; exit 1; }
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then printf 'aca_analytics_fqdn=%s\n' "$FQDN" >> "$GITHUB_OUTPUT"; fi
printf 'Matcher revision healthy: %s; readiness endpoint: https://%s/readyz\n' "$REVISION" "$FQDN"
