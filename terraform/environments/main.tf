# ============================================================================
# 1. READ PERSISTENT SHARED INFRASTRUCTURE
# ============================================================================

data "azurerm_container_registry" "shared_acr" {
  name                = var.shared_acr_name
  resource_group_name = var.shared_rg_name
}

data "azurerm_log_analytics_workspace" "shared_logs" {
  name                = var.shared_log_analytics_name
  resource_group_name = var.shared_rg_name
}

# ============================================================================
# 2. ENVIRONMENT RESOURCE GROUP
# ============================================================================

resource "azurerm_resource_group" "env_rg" {
  name     = "rg-${local.name_prefix}"
  location = var.location
  tags     = local.common_tags
}

# ============================================================================
# 3. NETWORK BOUNDARY, ACA INTEGRATION & PRIVATE ENDPOINTS
# ============================================================================

resource "azurerm_virtual_network" "vnet" {
  name                = "vnet-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  address_space       = ["10.0.0.0/16"]
  tags                = local.common_tags
}

resource "azurerm_subnet" "mysql_subnet" {
  name                 = "snet-mysql"
  resource_group_name  = azurerm_resource_group.env_rg.name
  virtual_network_name = azurerm_virtual_network.vnet.name
  address_prefixes     = ["10.0.2.0/24"]
  service_endpoints    = ["Microsoft.Storage"]

  delegation {
    name = "mysql-delegation"

    service_delegation {
      name = "Microsoft.DBforMySQL/flexibleServers"
      actions = [
        "Microsoft.Network/virtualNetworks/subnets/join/action"
      ]
    }
  }
}

# Dedicated /21 subnet required by the Consumption-only ACA environment model
# used by the pinned AzureRM 3.x provider baseline.
resource "azurerm_subnet" "aca_subnet" {
  name                 = "snet-aca"
  resource_group_name  = azurerm_resource_group.env_rg.name
  virtual_network_name = azurerm_virtual_network.vnet.name
  address_prefixes     = ["10.0.8.0/21"]

  delegation {
    name = "aca-delegation"

    service_delegation {
      name = "Microsoft.App/environments"
      actions = [
        "Microsoft.Network/virtualNetworks/subnets/join/action"
      ]
    }
  }
}

# Private endpoints must use a subnet separate from the delegated MySQL and ACA subnets.
resource "azurerm_subnet" "private_endpoints_subnet" {
  name                              = "snet-private-endpoints"
  resource_group_name               = azurerm_resource_group.env_rg.name
  virtual_network_name              = azurerm_virtual_network.vnet.name
  address_prefixes                  = ["10.0.3.0/24"]
  private_endpoint_network_policies = "Disabled"
}

# Private DNS zone used by the private MySQL Flexible Server.
resource "azurerm_private_dns_zone" "mysql_dns" {
  name                = "${local.name_prefix}.mysql.database.azure.com"
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "mysql_dns_link" {
  name                  = "vnetlink-mysql"
  private_dns_zone_name = azurerm_private_dns_zone.mysql_dns.name
  virtual_network_id    = azurerm_virtual_network.vnet.id
  resource_group_name   = azurerm_resource_group.env_rg.name
}

resource "azurerm_private_dns_zone" "redis_dns" {
  name                = "privatelink.redis.cache.windows.net"
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "redis_dns_link" {
  name                  = "vnetlink-redis"
  private_dns_zone_name = azurerm_private_dns_zone.redis_dns.name
  virtual_network_id    = azurerm_virtual_network.vnet.id
  resource_group_name   = azurerm_resource_group.env_rg.name
}

resource "azurerm_private_dns_zone" "acr_dns" {
  name                = "privatelink.azurecr.io"
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "acr_dns_link" {
  name                  = "vnetlink-acr"
  private_dns_zone_name = azurerm_private_dns_zone.acr_dns.name
  virtual_network_id    = azurerm_virtual_network.vnet.id
  resource_group_name   = azurerm_resource_group.env_rg.name
}

# Each isolated runtime environment receives a private endpoint to the shared ACR.
resource "azurerm_private_endpoint" "acr" {
  name                = "pe-acr-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  subnet_id           = azurerm_subnet.private_endpoints_subnet.id
  tags                = local.common_tags

  private_service_connection {
    name                           = "psc-acr-${local.name_prefix}"
    private_connection_resource_id = data.azurerm_container_registry.shared_acr.id
    subresource_names              = ["registry"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [azurerm_private_dns_zone.acr_dns.id]
  }
}

# ============================================================================
# 4. WORKLOAD MANAGED IDENTITY & RBAC
# ============================================================================

resource "azurerm_user_assigned_identity" "aca_identity" {
  name                = "id-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags
}

resource "azurerm_role_assignment" "acr_pull" {
  scope                = data.azurerm_container_registry.shared_acr.id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_user_assigned_identity.aca_identity.principal_id
}

data "azurerm_client_config" "current" {}

resource "azurerm_role_assignment" "sp_acr_pull" {
  count                = var.environment == "dev" ? 1 : 0
  scope                = data.azurerm_container_registry.shared_acr.id
  role_definition_name = "AcrPull"
  principal_id         = data.azurerm_client_config.current.object_id
}

resource "azurerm_role_assignment" "sp_acr_push" {
  count                = var.environment == "dev" ? 1 : 0
  scope                = data.azurerm_container_registry.shared_acr.id
  role_definition_name = "AcrPush"
  principal_id         = data.azurerm_client_config.current.object_id
}

# ============================================================================
# 5. DATA TIER: MYSQL FLEXIBLE SERVER & REDIS
# ============================================================================

resource "azurerm_mysql_flexible_server" "db" {
  # checkov:skip=CKV_AZURE_42:Auto-grow disabled in Sprint 1 baseline for student cost control.
  # checkov:skip=CKV_AZURE_98:Geo-redundant backup disabled for single-region academic MVP.
  name                   = "mysql-${local.name_prefix}"
  resource_group_name    = azurerm_resource_group.env_rg.name
  location               = azurerm_resource_group.env_rg.location
  administrator_login    = var.db_admin_username
  administrator_password = var.db_admin_password

  sku_name = "B_Standard_B1ms"
  version  = "8.0.21"

  # 100% Private VNet Delegation: No public IP, no public internet access
  delegated_subnet_id = azurerm_subnet.mysql_subnet.id
  private_dns_zone_id = azurerm_private_dns_zone.mysql_dns.id

  depends_on = [
    azurerm_private_dns_zone_virtual_network_link.mysql_dns_link
  ]

  storage {
    size_gb           = 20
    auto_grow_enabled = false
  }

  backup_retention_days        = 7
  geo_redundant_backup_enabled = false
  tags                         = local.common_tags

  lifecycle {
    ignore_changes = [
      zone,
      high_availability[0].standby_availability_zone
    ]
  }
}


resource "azurerm_mysql_flexible_database" "ewastedb" {
  name                = "ewastedb"
  resource_group_name = azurerm_resource_group.env_rg.name
  server_name         = azurerm_mysql_flexible_server.db.name
  charset             = "utf8mb4"
  collation           = "utf8mb4_unicode_ci"
}

resource "azurerm_redis_cache" "redis" {
  name                          = "redis-${local.name_prefix}"
  location                      = azurerm_resource_group.env_rg.location
  resource_group_name           = azurerm_resource_group.env_rg.name
  capacity                      = 0
  family                        = "C"
  sku_name                      = "Basic"
  non_ssl_port_enabled          = false
  minimum_tls_version           = "1.2"
  public_network_access_enabled = false
  tags                          = local.common_tags
}

resource "azurerm_private_endpoint" "redis" {
  name                = "pe-redis-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  subnet_id           = azurerm_subnet.private_endpoints_subnet.id
  tags                = local.common_tags

  private_service_connection {
    name                           = "psc-redis-${local.name_prefix}"
    private_connection_resource_id = azurerm_redis_cache.redis.id
    subresource_names              = ["redisCache"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [azurerm_private_dns_zone.redis_dns.id]
  }
}

# ============================================================================
# 5.1 EVENT STREAMING: AZURE EVENT HUBS (KAFKA PROTOCOL SURFACE)
# ============================================================================

# Event Hubs Namespace with Standard SKU (Kafka 1.0+ surface enabled by default)
resource "azurerm_eventhub_namespace" "kafka" {
  # checkov:skip=CKV_AZURE_168:Zone redundancy disabled for student single-region cost control.
  # checkov:skip=CKV_AZURE_167:TLS 1.2 minimum version enforced below.
  # checkov:skip=CKV_AZURE_166:Customer Managed Keys (CMK) disabled for academic MVP cost control.
  name                          = "evh-${local.name_prefix}"
  location                      = azurerm_resource_group.env_rg.location
  resource_group_name           = azurerm_resource_group.env_rg.name
  sku                           = "Standard"
  capacity                      = 1
  auto_inflate_enabled          = false
  minimum_tls_version           = "1.2"
  public_network_access_enabled = true
  tags                          = local.common_tags
}

# Canonical topics defined as Event Hubs
# Decision AD5: Lifecycle topics created with 3 partitions for multi-replica worker parallel consumption
locals {
  eventhub_topic_partitions = {
    "ewaste.batch.events"                = 3
    "ewaste.claim.events"                = 3
    "batch.collector.assigned"           = 3
    "batch.collection.completed"         = 3
    "batch.collection.failed"            = 1
    "ewaste.batch.events.matching.dlq.v1" = 1
  }
}

resource "azurerm_eventhub" "topics" {
  for_each = local.eventhub_topic_partitions

  name                = each.key
  namespace_name      = azurerm_eventhub_namespace.kafka.name
  resource_group_name = azurerm_resource_group.env_rg.name
  partition_count     = each.value
  message_retention   = 1
}

# Dedicated consumer group for Python matching-worker on 'ewaste.batch.events'
resource "azurerm_eventhub_consumer_group" "matching_worker" {
  name                = "matching-worker"
  namespace_name      = azurerm_eventhub_namespace.kafka.name
  eventhub_name       = azurerm_eventhub.topics["ewaste.batch.events"].name
  resource_group_name = azurerm_resource_group.env_rg.name
}

# Dedicated consumer group for trusted workflow readers on 'ewaste.batch.events'
resource "azurerm_eventhub_consumer_group" "workflow_readers" {
  name                = "workflow-readers"
  namespace_name      = azurerm_eventhub_namespace.kafka.name
  eventhub_name       = azurerm_eventhub.topics["ewaste.batch.events"].name
  resource_group_name = azurerm_resource_group.env_rg.name
}

# Dedicated consumer group for Python analytics-worker on 'ewaste.batch.events'
resource "azurerm_eventhub_consumer_group" "analytics_worker_batch" {
  name                = "analytics-worker"
  namespace_name      = azurerm_eventhub_namespace.kafka.name
  eventhub_name       = azurerm_eventhub.topics["ewaste.batch.events"].name
  resource_group_name = azurerm_resource_group.env_rg.name
}

# Dedicated consumer group for Python analytics-worker on 'ewaste.claim.events'
resource "azurerm_eventhub_consumer_group" "analytics_worker_claim" {
  name                = "analytics-worker"
  namespace_name      = azurerm_eventhub_namespace.kafka.name
  eventhub_name       = azurerm_eventhub.topics["ewaste.claim.events"].name
  resource_group_name = azurerm_resource_group.env_rg.name
}

# Shared authorization rule for application workloads (SASL/PLAIN connection string)
resource "azurerm_eventhub_namespace_authorization_rule" "app_auth" {
  name                = "auth-ewaste-workload"
  namespace_name      = azurerm_eventhub_namespace.kafka.name
  resource_group_name = azurerm_resource_group.env_rg.name

  listen = true
  send   = true
  manage = false
}

# Managed Identity RBAC assignments for passwordless Kafka communication from ACA
resource "azurerm_role_assignment" "eventhub_sender" {
  scope                = azurerm_eventhub_namespace.kafka.id
  role_definition_name = "Azure Event Hubs Data Sender"
  principal_id         = azurerm_user_assigned_identity.aca_identity.principal_id
}

resource "azurerm_role_assignment" "eventhub_receiver" {
  scope                = azurerm_eventhub_namespace.kafka.id
  role_definition_name = "Azure Event Hubs Data Receiver"
  principal_id         = azurerm_user_assigned_identity.aca_identity.principal_id
}

# ============================================================================
# 6. COMPUTE: AZURE CONTAINER APPS
# ============================================================================

resource "azurerm_container_app_environment" "aca_env" {
  name                       = "cae-${local.name_prefix}"
  location                   = azurerm_resource_group.env_rg.location
  resource_group_name        = azurerm_resource_group.env_rg.name
  log_analytics_workspace_id = data.azurerm_log_analytics_workspace.shared_logs.id
  infrastructure_subnet_id   = azurerm_subnet.aca_subnet.id
  tags                       = local.common_tags

  lifecycle {
    ignore_changes = [
      infrastructure_resource_group_name
    ]
  }
}

# 6.1 Backend API / workflow Container App
resource "azurerm_container_app" "api" {
  name                         = "aca-${local.name_prefix}-api"
  workload_profile_name        = "Consumption"
  container_app_environment_id = azurerm_container_app_environment.aca_env.id
  resource_group_name          = azurerm_resource_group.env_rg.name
  revision_mode                = "Single"
  tags                         = local.common_tags

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.aca_identity.id]
  }

  registry {
    server   = data.azurerm_container_registry.shared_acr.login_server
    identity = azurerm_user_assigned_identity.aca_identity.id
  }

  # Native ACA secrets (encrypted at rest by Azure)
  secret {
    name  = "db-password"
    value = var.db_admin_password
  }

  secret {
    name  = "redis-password"
    value = azurerm_redis_cache.redis.primary_access_key
  }

  secret {
    name  = "access-secret"
    value = var.auth_access_secret
  }

  secret {
    name  = "refresh-secret"
    value = var.auth_refresh_secret
  }

  secret {
    name  = "refresh-hash-secret"
    value = var.auth_refresh_hash_secret
  }

  secret {
    name  = "kafka-conn"
    value = azurerm_eventhub_namespace_authorization_rule.app_auth.primary_connection_string
  }

  # Shared with CD; IaC must retain the matcher workload identity on reapply.
  secret {
    name  = "matching-signing-key"
    value = var.matcher_signing_secret
  }

  secret {
    name  = "analytics-service-token"
    value = var.analytics_service_token
  }

  template {
    min_replicas = 1
    max_replicas = 2

    container {
      name   = "workflow-api"
      image  = var.image_digest
      cpu    = 0.5
      memory = "1Gi"

      env {
        name  = "EWASTE_MODE"
        value = var.environment == "dev" ? "development" : "production"
      }

      env {
        name  = "EWASTE_SERVER_PORT"
        value = ":8080"
      }

      env {
        name  = "EWASTE_SERVER_ALLOWED_ORIGINS"
        value = "https://aca-${local.name_prefix}-ui.${azurerm_container_app_environment.aca_env.default_domain}"
      }

      env {
        name  = "EWASTE_DATABASE_HOST"
        value = azurerm_mysql_flexible_server.db.fqdn
      }

      env {
        name  = "EWASTE_DATABASE_PORT"
        value = "3306"
      }

      env {
        name  = "EWASTE_DATABASE_NAME"
        value = azurerm_mysql_flexible_database.ewastedb.name
      }

      env {
        name  = "EWASTE_DATABASE_USER"
        value = var.db_admin_username
      }

      env {
        name        = "MYSQL_PASSWORD"
        secret_name = "db-password"
      }

      env {
        name        = "EWASTE_DATABASE_PASSWORD"
        secret_name = "db-password"
      }

      env {
        name  = "EWASTE_REDIS_ADDRESS"
        value = "${azurerm_redis_cache.redis.hostname}:6380"
      }

      env {
        name        = "REDIS_PASSWORD"
        secret_name = "redis-password"
      }

      env {
        name        = "EWASTE_REDIS_PASSWORD"
        secret_name = "redis-password"
      }

      env {
        name  = "EWASTE_REDIS_DB"
        value = "0"
      }

      env {
        name  = "EWASTE_REDIS_TLS_ENABLED"
        value = "true"
      }

      env {
        name  = "EWASTE_AUTH_ISSUER"
        value = "ewaste-workflow-api"
      }

      env {
        name        = "EWASTE_AUTH_ACCESS_SECRET"
        secret_name = "access-secret"
      }

      env {
        name        = "EWASTE_AUTH_REFRESH_SECRET"
        secret_name = "refresh-secret"
      }

      env {
        name        = "EWASTE_AUTH_REFRESH_HASH_SECRET"
        secret_name = "refresh-hash-secret"
      }

      env {
        name        = "EWASTE_AUTH_ANALYTICS_SERVICE_TOKEN"
        secret_name = "analytics-service-token"
      }

      env {
        name  = "EWASTE_AUTH_ACCESS_TTL"
        value = "15m"
      }

      env {
        name  = "EWASTE_AUTH_REFRESH_TTL"
        value = "24h"
      }

      env {
        name  = "EWASTE_RATE_LIMIT_REQUESTS"
        value = "10"
      }

      env {
        name  = "EWASTE_RATE_LIMIT_WINDOW"
        value = "1m"
      }

      env {
        name  = "EWASTE_LOGGING_LEVEL"
        value = "info"
      }

      env {
        name  = "EWASTE_LOGGING_FILE_PATH"
        value = "logs/workflow-api.log"
      }

      env {
        name  = "EWASTE_LOGGING_MAX_SIZE_MB"
        value = "10"
      }

      env {
        name  = "EWASTE_LOGGING_MAX_BACKUPS"
        value = "5"
      }

      env {
        name  = "EWASTE_LOGGING_MAX_AGE_DAYS"
        value = "30"
      }

      env {
        name  = "EWASTE_LOGGING_COMPRESS"
        value = "true"
      }

      env {
        name  = "EWASTE_LOGGING_CONSOLE"
        value = "true"
      }

      # ---- Matcher API facade (same settings as scripts/deploy-matcher.sh) ----
      env {
        name  = "EWASTE_MATCHING_ENABLED"
        value = "true"
      }

      env {
        name  = "EWASTE_MATCHING_ISSUER"
        value = "ewaste-matching-${var.environment}"
      }

      env {
        name  = "EWASTE_MATCHING_AUDIENCE"
        value = "ewaste-matching-api-${var.environment}"
      }

      env {
        name        = "EWASTE_MATCHING_SIGNING_SECRET"
        secret_name = "matching-signing-key"
      }

      # ---- Kafka / Event Hubs outbox relay ----
      env {
        name  = "EWASTE_KAFKA_ENABLED"
        value = "true"
      }

      env {
        name  = "EWASTE_KAFKA_BROKERS"
        value = "${azurerm_eventhub_namespace.kafka.name}.servicebus.windows.net:9093"
      }

      env {
        name  = "EWASTE_KAFKA_TLS_ENABLED"
        value = "true"
      }

      env {
        name  = "EWASTE_KAFKA_SASL_MECHANISM"
        value = "PLAIN"
      }

      env {
        name  = "EWASTE_KAFKA_SASL_USERNAME"
        value = "$ConnectionString"
      }

      env {
        name        = "KAFKA_CONNECTION_STRING"
        secret_name = "kafka-conn"
      }

      env {
        name  = "EWASTE_KAFKA_PUBLISH_TIMEOUT"
        value = "60s"
      }

      env {
        name  = "EWASTE_KAFKA_BATCH_SIZE"
        value = "1"
      }

      env {
        name  = "EWASTE_KAFKA_LEASE_DURATION"
        value = "180s"
      }

      env {
        name  = "EWASTE_KAFKA_LEADER_LEASE_DURATION"
        value = "300s"
      }

      # ---- Evidence Storage Adapter (Decision D2 & Backend Integration) ----
      env {
        name  = "STORAGE_ADAPTER_TYPE"
        value = "azure_blob"
      }

      env {
        name  = "AZURE_STORAGE_ACCOUNT"
        value = azurerm_storage_account.evidence.name
      }

      env {
        name  = "AZURE_STORAGE_CONTAINER"
        value = azapi_resource.evidence_private.name
      }

      env {
        name  = "AZURE_STORAGE_ENDPOINT"
        value = azurerm_storage_account.evidence.primary_blob_endpoint
      }

      env {
        name  = "AZURE_USE_MANAGED_ID"
        value = "true"
      }

      env {
        name  = "AZURE_CLIENT_ID"
        value = azurerm_user_assigned_identity.aca_identity.client_id
      }

      env {
        name  = "MAX_UPLOAD_SIZE_BYTES"
        value = "5242880"
      }

      # Backward compatibility aliases
      env {
        name  = "EWASTE_STORAGE_TYPE"
        value = "azure_blob"
      }

      env {
        name  = "EWASTE_STORAGE_AZURE_ACCOUNT_NAME"
        value = azurerm_storage_account.evidence.name
      }

      env {
        name  = "EWASTE_STORAGE_AZURE_CONTAINER_NAME"
        value = azapi_resource.evidence_private.name
      }

      env {
        name  = "EWASTE_STORAGE_AZURE_ENDPOINT"
        value = azurerm_storage_account.evidence.primary_blob_endpoint
      }
    }
  }

  ingress {
    external_enabled = true
    target_port      = 8080
    transport        = "auto"

    traffic_weight {
      percentage      = 100
      latest_revision = true
    }
  }

  lifecycle {
    ignore_changes = [
      template[0].container[0].image,
      template[0].container[0].memory,
      workload_profile_name
    ]
  }

  depends_on = [
    azurerm_role_assignment.acr_pull,
    azurerm_private_endpoint.acr,
    azurerm_mysql_flexible_server.db,
    azurerm_redis_cache.redis,
    azurerm_role_assignment.eventhub_sender,
    azurerm_eventhub_namespace_authorization_rule.app_auth,
    azurerm_role_assignment.storage_blob_data_contributor,
    azurerm_private_endpoint.storage_pe,
    azapi_resource.evidence_private
  ]
}

# 6.2 Frontend UI (Next.js) Container App
resource "azurerm_container_app" "ui" {
  name                         = "aca-${local.name_prefix}-ui"
  workload_profile_name        = "Consumption"
  container_app_environment_id = azurerm_container_app_environment.aca_env.id
  resource_group_name          = azurerm_resource_group.env_rg.name
  revision_mode                = "Single"
  tags                         = local.common_tags

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.aca_identity.id]
  }

  registry {
    server   = data.azurerm_container_registry.shared_acr.login_server
    identity = azurerm_user_assigned_identity.aca_identity.id
  }

  template {
    min_replicas = 1
    max_replicas = 2

    container {
      name   = "workflow-ui"
      image  = var.ui_image_digest
      cpu    = 0.5
      memory = "1Gi"

      env {
        name  = "APP_ENV"
        value = var.environment
      }

      env {
        name  = "NEXT_PUBLIC_API_URL"
        value = "https://${azurerm_container_app.api.ingress[0].fqdn}"
      }

      env {
        name  = "NEXT_PUBLIC_API_BASE_URL"
        value = "https://${azurerm_container_app.api.ingress[0].fqdn}"
      }

      env {
        name  = "API_PROXY_TARGET"
        value = "https://${azurerm_container_app.api.ingress[0].fqdn}"
      }

      env {
        name  = "NEXT_PUBLIC_USE_MOCK_AUTH"
        value = "false"
      }

      env {
        name  = "NODE_ENV"
        value = "production"
      }

      env {
        name  = "PORT"
        value = "3000"
      }
    }
  }

  ingress {
    external_enabled = true
    target_port      = 3000
    transport        = "auto"

    traffic_weight {
      percentage      = 100
      latest_revision = true
    }
  }

  lifecycle {
    ignore_changes = [
      template[0].container[0].image,
      template[0].container[0].memory,
      workload_profile_name
    ]
  }

  depends_on = [
    azurerm_role_assignment.acr_pull,
    azurerm_private_endpoint.acr,
    azurerm_container_app.api
  ]
}

# 6.3 Analytics & Matching Worker Container App
resource "azurerm_container_app" "analytics" {
  name                         = "aca-${local.name_prefix}-analytics"
  workload_profile_name        = "Consumption"
  container_app_environment_id = azurerm_container_app_environment.aca_env.id
  resource_group_name          = azurerm_resource_group.env_rg.name
  revision_mode                = "Single"
  tags                         = local.common_tags

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.aca_identity.id]
  }

  registry {
    server   = data.azurerm_container_registry.shared_acr.login_server
    identity = azurerm_user_assigned_identity.aca_identity.id
  }

  secret {
    name  = "kafka-conn"
    value = azurerm_eventhub_namespace_authorization_rule.app_auth.primary_connection_string
  }

  # Shared with CD; IaC must retain the matcher workload identity on reapply.
  secret {
    name  = "matching-signing-key"
    value = var.matcher_signing_secret
  }

  secret {
    name  = "analytics-service-token"
    value = var.analytics_service_token
  }

  template {
    min_replicas = 1
    max_replicas = 2

    container {
      name   = "workflow-analytics"
      image  = var.analytics_image_digest
      cpu    = 0.5
      memory = "1Gi"

      env {
        name  = "KAFKA_BOOTSTRAP_SERVERS"
        value = "${azurerm_eventhub_namespace.kafka.name}.servicebus.windows.net:9093"
      }

      env {
        name        = "KAFKA_CONNECTION_STRING"
        secret_name = "kafka-conn"
      }

      # Keep the worker startup contract aligned with scripts/deploy-matcher.sh.
      env {
        name        = "MATCHER_SIGNING_SECRET"
        secret_name = "matching-signing-key"
      }

      env {
        name  = "MATCHER_TOPIC"
        value = "ewaste.batch.events"
      }

      env {
        name  = "MATCHER_DLQ_TOPIC"
        value = "ewaste.batch.events.matching.dlq.v1"
      }

      env {
        name  = "MATCHER_GROUP_ID"
        value = "matching-worker-v1"
      }

      env {
        name  = "MATCHER_OFFSET_RESET"
        value = "earliest"
      }

      env {
        name  = "MATCHER_FACADE_URL"
        value = "https://${azurerm_container_app.api.ingress[0].fqdn}"
      }

      env {
        name  = "MATCHER_LOCAL_TEST"
        value = "0"
      }

      env {
        name  = "ANALYTICS_FACADE_URL"
        value = "https://${azurerm_container_app.api.ingress[0].fqdn}"
      }

      env {
        name  = "ANALYTICS_ENABLED"
        value = "true"
      }

      env {
        name  = "ANALYTICS_GROUP_ID"
        value = "analytics-processing-v1"
      }

      env {
        name        = "ANALYTICS_SERVICE_TOKEN"
        secret_name = "analytics-service-token"
      }

      env {
        name  = "MATCHER_TOKEN_ISSUER"
        value = "ewaste-matching-${var.environment}"
      }

      env {
        name  = "MATCHER_TOKEN_AUDIENCE"
        value = "ewaste-matching-api-${var.environment}"
      }

      env {
        name  = "MATCHER_MAX_POLL_MS"
        value = "300000"
      }

      env {
        name  = "MATCHER_SESSION_TIMEOUT_MS"
        value = "30000"
      }

      env {
        name  = "MATCHER_WORKERS"
        value = "1"
      }

      env {
        name  = "MATCHER_HTTP_TIMEOUT_SECONDS"
        value = "30"
      }

      env {
        name  = "MATCHER_MAX_RESPONSE_BYTES"
        value = "16777216"
      }

      env {
        name  = "MATCHER_MAX_RECORD_BYTES"
        value = "1000000"
      }

      env {
        name  = "MATCHER_DELIVERY_TIMEOUT_SECONDS"
        value = "120"
      }

      env {
        name  = "MATCHER_RETRY_BASE_SECONDS"
        value = "1"
      }

      env {
        name  = "MATCHER_RETRY_MAX_SECONDS"
        value = "30"
      }

      env {
        name  = "MATCHER_MAX_REFRESHES"
        value = "5"
      }

      env {
        name  = "MATCHER_HEALTH_PORT"
        value = "8000"
      }
    }
  }

  ingress {
    # Dev exposes /readyz for the CD smoke check. Stg/prod retain internal
    # ingress; deployment checks the exact revision's platform health there.
    external_enabled = var.environment == "dev" ? true : false
    target_port      = 8000
    transport        = "auto"

    traffic_weight {
      percentage      = 100
      latest_revision = true
    }
  }

  lifecycle {
    ignore_changes = [
      template[0].container[0].image,
      template[0].container[0].memory,
      workload_profile_name
    ]
  }

  depends_on = [
    azurerm_role_assignment.acr_pull,
    azurerm_role_assignment.eventhub_sender,
    azurerm_role_assignment.eventhub_receiver,
    azurerm_eventhub_namespace_authorization_rule.app_auth
  ]
}
