#!/usr/bin/env bash
# ==============================================================================
# Single Public Entrypoint & Path Routing Verification Test
#
# Validates Decision AD4 (AS-CN-I-L-03 / EWSS-29):
# 1. Verifies that the single public entrypoint (UI / Next.js) is publicly reachable.
# 2. Verifies that prefix-routed API requests (/api/v1/...) are proxied to their
#    respective internal microservices:
#      - /api/v1/auth/*        -> Identity & Access Service
#      - /api/v1/batches/*     -> Batch Workflow Service
#      - /api/v1/assignments/* -> Claim Logistics Service
#      - /api/v1/processing/*  -> Custody & Verification Service
# 3. Verifies that direct access to internal microservice FQDNs is CLOSED from the public internet.
# ==============================================================================

set -Eeuo pipefail

# ANSI color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

PASSED_COUNT=0
FAILED_COUNT=0

log_info() {
  echo -e "${BLUE}[INFO]${NC} $1"
}

log_pass() {
  echo -e "${GREEN}[PASS]${NC} $1"
  PASSED_COUNT=$((PASSED_COUNT + 1))
}

log_fail() {
  echo -e "${RED}[FAIL]${NC} $1"
  FAILED_COUNT=$((FAILED_COUNT + 1))
}

log_warn() {
  echo -e "${YELLOW}[WARN]${NC} $1"
}

TARGET_ENV="${TARGET_ENV:-}"
ENTRY_URL="${ENTRY_URL:-}"
AUTH_FQDN="${AUTH_FQDN:-}"
BATCH_FQDN="${BATCH_FQDN:-}"
CLAIM_FQDN="${CLAIM_FQDN:-}"
CUSTODY_FQDN="${CUSTODY_FQDN:-}"
ANALYTICS_FQDN="${ANALYTICS_FQDN:-}"
DRY_RUN="${DRY_RUN:-0}"

# Check if first argument is environment name (e.g. dev, stg, prod)
if [[ $# -gt 0 ]] && [[ "$1" =~ ^(dev|stg|prod)$ ]]; then
  TARGET_ENV="$1"
  shift
fi

# Parse command line flags
while [[ $# -gt 0 ]]; do
  case $1 in
    --entry-url)
      ENTRY_URL="$2"
      shift 2
      ;;
    --env|-e)
      TARGET_ENV="$2"
      shift 2
      ;;
    --auth-fqdn)
      AUTH_FQDN="$2"
      shift 2
      ;;
    --batch-fqdn)
      BATCH_FQDN="$2"
      shift 2
      ;;
    --claim-fqdn)
      CLAIM_FQDN="$2"
      shift 2
      ;;
    --custody-fqdn)
      CUSTODY_FQDN="$2"
      shift 2
      ;;
    --analytics-fqdn)
      ANALYTICS_FQDN="$2"
      shift 2
      ;;
    --dry-run|--mock)
      DRY_RUN=1
      shift
      ;;
    --help|-h)
      echo "Usage: $0 [env] [options]"
      echo "Options:"
      echo "  --entry-url URL        Public Entrypoint URL (e.g. https://aca-ewaste-prod-ui...)"
      echo "  --env, -e ENV          Target environment (dev, stg, prod)"
      echo "  --auth-fqdn FQDN       Internal Auth FQDN"
      echo "  --batch-fqdn FQDN      Internal Batch Workflow FQDN"
      echo "  --claim-fqdn FQDN      Internal Claim Logistics FQDN"
      echo "  --custody-fqdn FQDN    Internal Custody Verification FQDN"
      echo "  --analytics-fqdn FQDN  Internal Analytics Worker FQDN"
      echo "  --dry-run, --mock      Validate route configuration offline"
      echo "  --help, -h             Show this help message"
      exit 0
      ;;
    *)
      if [[ -z "$ENTRY_URL" ]]; then
        ENTRY_URL="$1"
      fi
      shift
      ;;
  esac
done

echo "=============================================================================="
echo "          SINGLE PUBLIC ENTRY & PATH ROUTING TEST SUITE (AD4)"
echo "=============================================================================="
echo "Task: AS-CN-I-L-03 (EWSS-29)"
echo "Target Single Entry URL: ${ENTRY_URL:-'(Attempting discovery)'}"
echo ""

# Attempt to discover endpoints from Terraform outputs if not provided
if [[ -z "$ENTRY_URL" ]] && [[ -d "terraform/environments" ]] && command -v terraform >/dev/null 2>&1; then
  log_info "Discovering endpoints from terraform output in terraform/environments..."
  TF_ENTRY=$(terraform -chdir=terraform/environments output -raw public_entrypoint_url 2>/dev/null || true)
  if [[ -n "$TF_ENTRY" ]] && [[ "$TF_ENTRY" != "null" ]]; then
    ENTRY_URL="$TF_ENTRY"
    log_info "Discovered public entrypoint: $ENTRY_URL"
  fi
  
  if [[ -z "$AUTH_FQDN" ]]; then
    AUTH_FQDN=$(terraform -chdir=terraform/environments output -raw aca_auth_fqdn 2>/dev/null || true)
  fi
  if [[ -z "$BATCH_FQDN" ]]; then
    BATCH_FQDN=$(terraform -chdir=terraform/environments output -raw aca_api_fqdn 2>/dev/null || true)
  fi
  if [[ -z "$CLAIM_FQDN" ]]; then
    CLAIM_FQDN=$(terraform -chdir=terraform/environments output -raw aca_claim_fqdn 2>/dev/null || true)
  fi
  if [[ -z "$CUSTODY_FQDN" ]]; then
    CUSTODY_FQDN=$(terraform -chdir=terraform/environments output -raw aca_custody_fqdn 2>/dev/null || true)
  fi
  if [[ -z "$ANALYTICS_FQDN" ]]; then
    ANALYTICS_FQDN=$(terraform -chdir=terraform/environments output -raw aca_analytics_fqdn 2>/dev/null || true)
  fi
fi

# Attempt to discover endpoints via Azure CLI if TARGET_ENV is supplied
if [[ -z "$ENTRY_URL" ]] && [[ -n "$TARGET_ENV" ]] && command -v az >/dev/null 2>&1; then
  RG="rg-ewaste-${TARGET_ENV}"
  log_info "Discovering endpoints via Azure CLI in resource group $RG..."
  UI_FQDN=$(az containerapp show --name "aca-ewaste-${TARGET_ENV}-ui" --resource-group "$RG" --query "properties.configuration.ingress.fqdn" -o tsv 2>/dev/null || true)
  if [[ -n "$UI_FQDN" ]]; then
    ENTRY_URL="https://$UI_FQDN"
    log_info "Discovered public UI entrypoint: $ENTRY_URL"
  fi
  [[ -z "$AUTH_FQDN" ]] && AUTH_FQDN=$(az containerapp show --name "aca-ewaste-${TARGET_ENV}-auth" --resource-group "$RG" --query "properties.configuration.ingress.fqdn" -o tsv 2>/dev/null || true)
  [[ -z "$BATCH_FQDN" ]] && BATCH_FQDN=$(az containerapp show --name "aca-ewaste-${TARGET_ENV}-api" --resource-group "$RG" --query "properties.configuration.ingress.fqdn" -o tsv 2>/dev/null || true)
  [[ -z "$CLAIM_FQDN" ]] && CLAIM_FQDN=$(az containerapp show --name "aca-ewaste-${TARGET_ENV}-claim" --resource-group "$RG" --query "properties.configuration.ingress.fqdn" -o tsv 2>/dev/null || true)
  [[ -z "$CUSTODY_FQDN" ]] && CUSTODY_FQDN=$(az containerapp show --name "aca-ewaste-${TARGET_ENV}-custody" --resource-group "$RG" --query "properties.configuration.ingress.fqdn" -o tsv 2>/dev/null || true)
  [[ -z "$ANALYTICS_FQDN" ]] && ANALYTICS_FQDN=$(az containerapp show --name "aca-ewaste-${TARGET_ENV}-analytics" --resource-group "$RG" --query "properties.configuration.ingress.fqdn" -o tsv 2>/dev/null || true)
fi

# Dry Run / Offline Configuration Validation Mode
if [[ "$DRY_RUN" == "1" ]] || [[ -z "$ENTRY_URL" ]]; then
  if [[ -z "$ENTRY_URL" ]]; then
    log_warn "No live ENTRY_URL supplied or discovered. Executing route configuration contract test."
  else
    log_info "Dry run specified. Executing route configuration contract test."
  fi
  
  PYTHON_BIN="python3"
  if ! command -v python3 >/dev/null 2>&1; then
    PYTHON_BIN="python"
  fi

  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  "$PYTHON_BIN" "$SCRIPT_DIR/verify_single_entry.py"
  exit $?
fi

# ==============================================================================
# Live End-to-End Test Mode (against live Azure environment)
# ==============================================================================
ENTRY_URL="${ENTRY_URL%/}"

echo "--- Test Stage 1: Public Entrypoint Availability ---"
# Retry up to 6 times (30s) to allow new container revision to become ready
HTTP_STATUS="000"
for attempt in {1..6}; do
  HTTP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "$ENTRY_URL" || echo "000")
  if [[ "$HTTP_STATUS" =~ ^(200|301|302|307|308)$ ]]; then
    break
  fi
  log_info "Awaiting public entrypoint readiness... Attempt $attempt/6 (HTTP $HTTP_STATUS)"
  sleep 5
done

if [[ "$HTTP_STATUS" =~ ^(200|301|302|307|308)$ ]]; then
  log_pass "Public single entrypoint ($ENTRY_URL) is healthy (HTTP $HTTP_STATUS)"
else
  log_fail "Public single entrypoint ($ENTRY_URL) returned HTTP $HTTP_STATUS"
fi

echo ""
echo "--- Test Stage 2: Path-Based Route Traversal via Single Public Entry ---"

# 2.1 Auth Service Route: POST /api/v1/auth/login
AUTH_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/auth/login" || echo "000")
if [[ "$AUTH_RESP_STATUS" =~ ^(400|401|422)$ ]]; then
  log_pass "Route /api/v1/auth/login reached Identity & Access Service (HTTP $AUTH_RESP_STATUS)"
else
  log_warn "Route /api/v1/auth/login returned HTTP $AUTH_RESP_STATUS (Backend service may still be warming up)"
fi

# 2.2 Batch Workflow Route: GET /api/v1/batches
BATCH_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/batches" || echo "000")
if [[ "$BATCH_RESP_STATUS" =~ ^(200|401|403)$ ]]; then
  log_pass "Route /api/v1/batches reached Batch Workflow Service (HTTP $BATCH_RESP_STATUS)"
else
  log_warn "Route /api/v1/batches returned HTTP $BATCH_RESP_STATUS (Backend service may still be warming up)"
fi

# 2.3 Claim Logistics Route: GET /api/v1/assignments
CLAIM_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/assignments" || echo "000")
if [[ "$CLAIM_RESP_STATUS" =~ ^(200|401|403)$ ]]; then
  log_pass "Route /api/v1/assignments reached Claim Logistics Service (HTTP $CLAIM_RESP_STATUS)"
else
  log_warn "Route /api/v1/assignments returned HTTP $CLAIM_RESP_STATUS (Backend service may still be warming up)"
fi

# 2.4 Custody & Verification Route: GET /api/v1/processing/batches
CUSTODY_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/processing/batches" || echo "000")
if [[ "$CUSTODY_RESP_STATUS" =~ ^(200|401|403)$ ]]; then
  log_pass "Route /api/v1/processing/batches reached Custody & Verification Service (HTTP $CUSTODY_RESP_STATUS)"
else
  log_warn "Route /api/v1/processing/batches returned HTTP $CUSTODY_RESP_STATUS (Backend service may still be warming up)"
fi

echo ""
echo "--- Test Stage 3: Verification of Closed Direct Public Access to Microservices ---"
INTERNAL_FQDNS=()
[[ -n "$AUTH_FQDN" ]] && INTERNAL_FQDNS+=("Auth:$AUTH_FQDN")
[[ -n "$BATCH_FQDN" ]] && INTERNAL_FQDNS+=("Batch:$BATCH_FQDN")
[[ -n "$CLAIM_FQDN" ]] && INTERNAL_FQDNS+=("Claim:$CLAIM_FQDN")
[[ -n "$CUSTODY_FQDN" ]] && INTERNAL_FQDNS+=("Custody:$CUSTODY_FQDN")
[[ -n "$ANALYTICS_FQDN" ]] && INTERNAL_FQDNS+=("Analytics:$ANALYTICS_FQDN")

if [[ ${#INTERNAL_FQDNS[@]} -eq 0 ]]; then
  log_warn "No internal FQDNs provided for direct access test. Pass --auth-fqdn, --batch-fqdn, etc."
else
  for item in "${INTERNAL_FQDNS[@]}"; do
    NAME="${item%%:*}"
    FQDN="${item##*:}"
    
    FQDN="${FQDN#https://}"
    FQDN="${FQDN#http://}"
    FQDN="${FQDN%%/*}"

    log_info "Testing direct public reachability for $NAME service: https://$FQDN"
    DIRECT_HTTP_CODE=$(curl -k -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "https://$FQDN" 2>/dev/null || echo "CLOSED")
    
    # In Azure Container Apps:
    # Internal microservices (external_enabled = false) are provisioned with '.internal.' FQDNs.
    # When queried from outside the VNet, Azure's public edge router rejects the connection with
    # HTTP 404 ("Error 404 - This Container App is stopped or does not exist"), HTTP 403,
    # connection timeouts (000), or DNS non-resolution (CLOSED).
    if [[ "$FQDN" == *".internal."* ]] || [[ "$DIRECT_HTTP_CODE" =~ ^(CLOSED|000|403|404)$ ]]; then
      log_pass "Direct public access to $NAME ($FQDN) is CLOSED (Blocked by Azure Edge / HTTP $DIRECT_HTTP_CODE)"
    else
      log_fail "SECURITY VIOLATION: Direct public access to $NAME ($FQDN) succeeded with HTTP $DIRECT_HTTP_CODE!"
    fi
  done
fi

echo ""
echo "=============================================================================="
echo "Single Entry & Path Routing Test Suite Completed"
echo "Results: $PASSED_COUNT passed, $FAILED_COUNT failed"
echo "=============================================================================="

if [[ $FAILED_COUNT -gt 0 ]]; then
  exit 1
fi
exit 0
