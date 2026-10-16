#!/usr/bin/env bash
# public: hosted runner, ARM settings + authenticated public-network denial.
# private: existing azure-vnet runner, its managed identity + blob round trip.
# No account keys, firewall changes or business evidence are used.
set -euo pipefail
TARGET_ENV="${1:-dev}"
PHASE="${2:-public}"
[[ "$TARGET_ENV" =~ ^(dev|stg|prod)$ ]] || { echo 'Invalid environment.' >&2; exit 1; }
[[ "$PHASE" =~ ^(public|private)$ ]] || { echo 'Expected public or private phase.' >&2; exit 1; }
RG="rg-ewaste-${TARGET_ENV}"
ACCOUNT="stgewastescalable${TARGET_ENV}"
CONTAINER="evidence-private"
URL="https://${ACCOUNT}.blob.core.windows.net/${CONTAINER}"
TMP=$(mktemp -d)
BLOB=""
cleanup() {
  local result=$?
  trap - EXIT
  if [[ -n "$BLOB" ]]; then
    az storage blob delete --account-name "$ACCOUNT" --container-name "$CONTAINER" \
      --name "$BLOB" --auth-mode login --only-show-errors -o none || result=1
  fi
  rm -rf -- "$TMP"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'echo "FAIL: evidence storage verification at line $LINENO" >&2' ERR
fail() { echo "FAIL: $*" >&2; exit 1; }
echo "Evidence storage: environment=$TARGET_ENV phase=$PHASE checked_at=$(date -u +%FT%TZ)"

if [[ "$PHASE" == public ]]; then
  ACCOUNT_JSON=$(az storage account show --name "$ACCOUNT" --resource-group "$RG" -o json)
  jq -e '.publicNetworkAccess == "Disabled" and .allowBlobPublicAccess == false and .allowSharedKeyAccess == false' <<< "$ACCOUNT_JSON" >/dev/null
  SA_ID=$(jq -er '.id' <<< "$ACCOUNT_JSON")
  az rest --method get --url "https://management.azure.com${SA_ID}/blobServices/default?api-version=2023-01-01" \
    | jq -e '.properties.isVersioningEnabled == true and .properties.deleteRetentionPolicy.enabled == true and .properties.deleteRetentionPolicy.days == 7' >/dev/null
  az rest --method get --url "https://management.azure.com${SA_ID}/blobServices/default/containers/${CONTAINER}?api-version=2023-01-01" \
    | jq -e '(.properties.publicAccess // "None") == "None"' >/dev/null
  echo 'PASS: private account/container, shared keys disabled, versioning and seven-day soft delete.'

  az network private-endpoint show --name "pe-stgewastescalable-${TARGET_ENV}" --resource-group "$RG" \
    | jq -e --arg id "$SA_ID" '.privateLinkServiceConnections | any(.privateLinkServiceConnectionState.status == "Approved" and .privateLinkServiceId == $id and (.groupIds | index("blob") != null))' >/dev/null
  VNET_ID=$(az network vnet show --name "vnet-ewaste-${TARGET_ENV}" --resource-group "$RG" --query id -o tsv)
  [[ -n "$VNET_ID" ]] || fail 'VNet ID is missing.'
  az network private-dns link vnet show --name vnetlink-blob --zone-name privatelink.blob.core.windows.net --resource-group "$RG" \
    | jq -e --arg id "$VNET_ID" '.virtualNetwork.id == $id and .virtualNetworkLinkState == "Completed"' >/dev/null
  echo 'PASS: approved Blob private endpoint and DNS link to the expected VNet.'

  IDENTITY=$(az identity show --name "id-ewaste-${TARGET_ENV}" --resource-group "$RG" -o json)
  CLIENT_ID=$(jq -er '.clientId' <<< "$IDENTITY")
  PRINCIPAL_ID=$(jq -er '.principalId' <<< "$IDENTITY")
  IDENTITY_ID=$(jq -er '.id' <<< "$IDENTITY")
  az role assignment list --scope "$SA_ID" --include-inherited \
    | jq -e --arg principal "$PRINCIPAL_ID" 'any(.principalId == $principal and .roleDefinitionName == "Storage Blob Data Contributor")' >/dev/null
  az containerapp show --name "aca-ewaste-${TARGET_ENV}-api" --resource-group "$RG" \
    | jq -e --arg id "$IDENTITY_ID" --arg client "$CLIENT_ID" --arg account "$ACCOUNT" '
      (.identity.userAssignedIdentities | has($id)) and
      ([.properties.template.containers[] | select(.name == "workflow-api") | .env[]] | map({key:.name,value:.value}) | from_entries |
       .STORAGE_ADAPTER_TYPE == "azure_blob" and .AZURE_STORAGE_ACCOUNT == $account and
       .AZURE_STORAGE_CONTAINER == "evidence-private" and .AZURE_STORAGE_ENDPOINT == ("https://" + $account + ".blob.core.windows.net/") and
       .AZURE_USE_MANAGED_ID == "true" and .AZURE_CLIENT_ID == $client and .MAX_UPLOAD_SIZE_BYTES == "5242880")' >/dev/null
  echo 'PASS: API identity, existing Blob Contributor assignment and adapter settings.'

  TOKEN=$(az account get-access-token --resource https://storage.azure.com/ --query accessToken -o tsv)
  [[ -n "$TOKEN" ]] || fail 'No storage access token.'
  STATUS=$(curl --silent --show-error --max-time 20 -o "$TMP/denied.xml" -w '%{http_code}' \
    -H "Authorization: Bearer $TOKEN" -H 'x-ms-version: 2023-11-03' "${URL}?restype=container&comp=list&maxresults=1")
  unset TOKEN
  [[ "$STATUS" == 403 ]] || fail "Public request returned HTTP $STATUS; expected 403."
  echo 'PASS: authenticated request from the hosted runner denied (HTTP 403); public access remains disabled.'
else
  CLIENT_ID=$(az identity show --name "id-runner-ewaste-${TARGET_ENV}" --resource-group "$RG" --query clientId -o tsv)
  [[ -n "$CLIENT_ID" && "$CLIENT_ID" != null ]] || fail 'Existing VNet runner identity is missing.'
  # Keep this job login separate from the persistent self-hosted runner CLI cache.
  export AZURE_CONFIG_DIR="$TMP/azure"
  az login --identity --client-id "$CLIENT_ID" --output none
  python3 - "${ACCOUNT}.blob.core.windows.net" <<'PY'
import ipaddress, socket, sys
addresses = {entry[4][0] for entry in socket.getaddrinfo(sys.argv[1], 443, type=socket.SOCK_STREAM)}
if not addresses or not all(ipaddress.ip_address(ip).is_private for ip in addresses):
    raise SystemExit("FAIL: Blob hostname must resolve only to private addresses")
print("PASS: private DNS resolution: " + ", ".join(sorted(addresses)))
PY
  PROBE="verification/$(python3 -c 'import uuid; print(uuid.uuid4())').txt"
  printf 'Synthetic storage connectivity probe: %s\n' "$PROBE" > "$TMP/upload.txt"
  # Allow the new RBAC assignment a bounded propagation window.
  for attempt in {1..12}; do
    if az storage blob upload --account-name "$ACCOUNT" --container-name "$CONTAINER" --name "$PROBE" \
      --file "$TMP/upload.txt" --auth-mode login --overwrite false --only-show-errors -o none; then
      BLOB="$PROBE"
      break
    fi
    [[ "$attempt" -lt 12 ]] || fail 'Managed identity upload failed after 12 attempts.'
    sleep 10
  done
  az storage blob download --account-name "$ACCOUNT" --container-name "$CONTAINER" --name "$BLOB" \
    --file "$TMP/download.txt" --auth-mode login --only-show-errors -o none
  cmp "$TMP/upload.txt" "$TMP/download.txt"
  echo 'PASS: VNet runner managed identity uploaded and downloaded identical bytes.'
  STATUS=$(curl --silent --show-error --max-time 20 -o /dev/null -w '%{http_code}' \
    -H 'x-ms-version: 2023-11-03' "${URL}/${BLOB}")
  case "$STATUS" in 401|403|404) ;; *) fail "Anonymous read of the existing blob returned HTTP $STATUS." ;; esac
  echo "PASS: anonymous private-network read denied (HTTP $STATUS)."
  az storage blob delete --account-name "$ACCOUNT" --container-name "$CONTAINER" --name "$BLOB" \
    --auth-mode login --only-show-errors -o none
  BLOB=""
  echo 'PASS: synthetic probe deleted (subject to seven-day soft-delete retention).'
fi
# A failure above exits nonzero; never substitute successful states or addresses.
echo "PASS: evidence storage $PHASE verification completed."
