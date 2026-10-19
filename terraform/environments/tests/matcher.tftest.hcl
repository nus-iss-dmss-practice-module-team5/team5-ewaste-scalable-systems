# Uses Terraform 1.8 mock providers only: no Azure calls or real credentials.
mock_provider "azurerm" {
  mock_data "azurerm_container_registry" {
    defaults = {
      id           = "/subscriptions/00000000-0000-4000-8000-000000000001/resourceGroups/test/providers/Microsoft.ContainerRegistry/registries/testregistry"
      login_server = "testregistry.azurecr.io"
    }
  }
  mock_data "azurerm_log_analytics_workspace" {
    defaults = {
      id = "/subscriptions/00000000-0000-4000-8000-000000000001/resourceGroups/test/providers/Microsoft.OperationalInsights/workspaces/test"
    }
  }
}
mock_provider "random" {}
mock_provider "tls" {}
mock_provider "azapi" {}

variables {
  tenant_id                 = "00000000-0000-4000-8000-000000000001"
  db_admin_password         = "Local-test-database-123!"
  auth_access_secret        = "local-test-access-secret-at-least-32-characters"
  auth_refresh_secret       = "local-test-refresh-secret-at-least-32-characters"
  auth_refresh_hash_secret  = "local-test-refresh-hash-at-least-32-characters"
  matcher_signing_secret    = "local-test-matcher-secret-at-least-32-characters"
  analytics_service_token   = "local-test-analytics-token-at-least-32-characters"
  enable_self_hosted_runner = false
}

run "matcher_configuration_survives_iac" {
  command = plan

  assert {
    condition = alltrue([
      for app in [azurerm_container_app.api, azurerm_container_app.analytics] :
      one([for s in app.secret : s.value if s.name == "matching-signing-key"]) == var.matcher_signing_secret
    ])
    error_message = "API and worker must retain the same dedicated matcher signing secret."
  }

  assert {
    condition = alltrue([
      for app in [azurerm_container_app.api, azurerm_container_app.analytics] :
      one([for s in app.secret : s.value if s.name == "analytics-service-token"]) == var.analytics_service_token
    ])
    error_message = "API and worker must retain the same dedicated analytics service token."
  }

  assert {
    condition = alltrue([
      for key, value in {
        EWASTE_MATCHING_ENABLED  = "true"
        EWASTE_MATCHING_ISSUER   = "ewaste-matching-dev"
        EWASTE_MATCHING_AUDIENCE = "ewaste-matching-api-dev"
        EWASTE_KAFKA_BATCH_SIZE  = "1"
      } : one([for e in azurerm_container_app.api.template[0].container[0].env : e.value if e.name == key]) == value
    ])
    error_message = "IaC must keep the matching facade enabled and preserve CD's issuer, audience and publisher settings."
  }

  assert {
    condition = alltrue([
      for key in [
        "MATCHER_FACADE_URL", "MATCHER_LOCAL_TEST", "MATCHER_TOPIC", "MATCHER_DLQ_TOPIC",
        "MATCHER_GROUP_ID", "MATCHER_OFFSET_RESET", "MATCHER_TOKEN_ISSUER", "MATCHER_TOKEN_AUDIENCE",
        "MATCHER_HTTP_TIMEOUT_SECONDS", "MATCHER_MAX_RESPONSE_BYTES", "MATCHER_MAX_RECORD_BYTES",
        "MATCHER_DELIVERY_TIMEOUT_SECONDS", "MATCHER_RETRY_BASE_SECONDS", "MATCHER_RETRY_MAX_SECONDS",
        "MATCHER_MAX_REFRESHES", "MATCHER_MAX_POLL_MS", "MATCHER_SESSION_TIMEOUT_MS", "MATCHER_WORKERS", "MATCHER_HEALTH_PORT"
      ] : length([for e in azurerm_container_app.analytics.template[0].container[0].env : e.value if e.name == key]) == 1
    ])
    error_message = "IaC must supply every required worker startup setting, exactly once."
  }

  assert {
    condition = (
      one([for e in azurerm_container_app.analytics.template[0].container[0].env : e.value if e.name == "MATCHER_GROUP_ID"]) == "matching-worker-v1" &&
      one([for e in azurerm_container_app.analytics.template[0].container[0].env : e.secret_name if e.name == "MATCHER_SIGNING_SECRET"]) == "matching-signing-key" &&
      one([for e in azurerm_container_app.api.template[0].container[0].env : e.secret_name if e.name == "EWASTE_MATCHING_SIGNING_SECRET"]) == "matching-signing-key"
    )
    error_message = "Worker must preserve the existing consumer group and both applications must use secret references."
  }

  assert {
    condition = (
      one([for e in azurerm_container_app.analytics.template[0].container[0].env : e.value if e.name == "ANALYTICS_ENABLED"]) == "true" &&
      one([for e in azurerm_container_app.analytics.template[0].container[0].env : e.value if e.name == "ANALYTICS_GROUP_ID"]) == "analytics-processing-v1" &&
      one([for e in azurerm_container_app.analytics.template[0].container[0].env : e.secret_name if e.name == "ANALYTICS_SERVICE_TOKEN"]) == "analytics-service-token" &&
      one([for e in azurerm_container_app.api.template[0].container[0].env : e.secret_name if e.name == "EWASTE_AUTH_ANALYTICS_SERVICE_TOKEN"]) == "analytics-service-token"
    )
    error_message = "Processing must use separate offsets and both applications must reference the analytics service token secret."
  }
}

run "shared_user_token_issuer" {
  command = plan

  assert {
    condition = alltrue([
      for app in [azurerm_container_app.auth, azurerm_container_app.api, azurerm_container_app.claim, azurerm_container_app.custody] :
      one([for e in app.template[0].container[0].env : e.value if e.name == "EWASTE_AUTH_ISSUER"]) == "ewaste-identity-access"
    ])
    error_message = "All user-facing APIs must trust the identity service's JWT issuer; service-specific issuers reject valid login tokens."
  }
}

run "reject_short_matcher_key" {
  command = plan
  variables {
    matcher_signing_secret = "short"
  }
  expect_failures = [var.matcher_signing_secret]
}

run "private_evidence_configuration" {
  command = plan

  assert {
    condition = (
      !azurerm_storage_account.evidence.public_network_access_enabled &&
      !azurerm_storage_account.evidence.allow_nested_items_to_be_public &&
      !azurerm_storage_account.evidence.shared_access_key_enabled &&
      azapi_resource.evidence_private.body.properties.publicAccess == "None" &&
      azapi_update_resource.evidence_blob_protection.body.properties.isVersioningEnabled &&
      azapi_update_resource.evidence_blob_protection.body.properties.deleteRetentionPolicy.enabled &&
      azapi_update_resource.evidence_blob_protection.body.properties.deleteRetentionPolicy.days == 7
    )
    error_message = "Evidence must remain private and keyless with versioning and seven-day soft delete."
  }

  assert {
    condition = (
      length([for e in azurerm_container_app.api.template[0].container[0].env : e if e.name == "AZURE_CLIENT_ID"]) == 1 &&
      one([for e in azurerm_container_app.api.template[0].container[0].env : e.value if e.name == "STORAGE_ADAPTER_TYPE"]) == "azure_blob"
    )
    error_message = "The Azure adapter must explicitly select the API's user-assigned identity."
  }
}
