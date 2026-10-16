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

# Default configuration (can be overridden via environment variables or CLI flags)
ENTRY_URL="${ENTRY_URL:-}"
AUTH_FQDN="${AUTH_FQDN:-}"
BATCH_FQDN="${BATCH_FQDN:-}"
CLAIM_FQDN="${CLAIM_FQDN:-}"
CUSTODY_FQDN="${CUSTODY_FQDN:-}"
ANALYTICS_FQDN="${ANALYTICS_FQDN:-}"
DRY_RUN="${DRY_RUN:-0}"

# Parse command line flags
while [[ $# -gt 0 ]]; do
  case $1 in
    --entry-url)
      ENTRY_URL="$2"
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
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --entry-url URL        Public Entrypoint URL (e.g. https://aca-ewaste-prod-ui...)"
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
echo "Target Single Entry URL: ${ENTRY_URL:-'(Not set - attempting Terraform discovery)'}"
echo ""

# Attempt to discover endpoints from Terraform outputs if not provided
if [[ -z "$ENTRY_URL" ]] && [[ -d "terraform/environments" ]] && command -v terraform >/dev/null 2>&1; then
  log_info "Discovering endpoints from terraform output in terraform/environments..."
  TF_ENTRY=$(terraform -chdir=terraform/environments output -raw public_entrypoint_url 2>/dev/null || true)
  if [[ -n "$TF_ENTRY" ]]; then
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
HTTP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" --connect-timeout 10 "$ENTRY_URL" || echo "000")
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
  log_fail "Route /api/v1/auth/login unexpected response (HTTP $AUTH_RESP_STATUS)"
fi

# 2.2 Batch Workflow Route: GET /api/v1/batches
BATCH_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/batches" || echo "000")
if [[ "$BATCH_RESP_STATUS" =~ ^(200|401|403)$ ]]; then
  log_pass "Route /api/v1/batches reached Batch Workflow Service (HTTP $BATCH_RESP_STATUS)"
else
  log_fail "Route /api/v1/batches unexpected response (HTTP $BATCH_RESP_STATUS)"
fi

# 2.3 Claim Logistics Route: GET /api/v1/assignments
CLAIM_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/assignments" || echo "000")
if [[ "$CLAIM_RESP_STATUS" =~ ^(200|401|403)$ ]]; then
  log_pass "Route /api/v1/assignments reached Claim Logistics Service (HTTP $CLAIM_RESP_STATUS)"
else
  log_fail "Route /api/v1/assignments unexpected response (HTTP $CLAIM_RESP_STATUS)"
fi

# 2.4 Custody & Verification Route: GET /api/v1/processing/batches
CUSTODY_RESP_STATUS=$(curl -k -s -o /dev/null -w "%{http_code}" \
  --connect-timeout 10 \
  "$ENTRY_URL/api/v1/processing/batches" || echo "000")
if [[ "$CUSTODY_RESP_STATUS" =~ ^(200|401|403)$ ]]; then
  log_pass "Route /api/v1/processing/batches reached Custody & Verification Service (HTTP $CUSTODY_RESP_STATUS)"
else
  log_fail "Route /api/v1/processing/batches unexpected response (HTTP $CUSTODY_RESP_STATUS)"
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

    if [[ "$DIRECT_HTTP_CODE" == "CLOSED" ]] || [[ "$DIRECT_HTTP_CODE" == "000" ]] || [[ "$DIRECT_HTTP_CODE" == "403" ]]; then
      log_pass "Direct public access to $NAME ($FQDN) is CLOSED (Result: $DIRECT_HTTP_CODE)"
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
