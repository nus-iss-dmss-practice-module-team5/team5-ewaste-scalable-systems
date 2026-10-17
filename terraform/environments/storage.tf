# ============================================================================
# AZURE BLOB STORAGE FOR PHYSICAL VERIFICATION EVIDENCE (DECISION D2)
# ============================================================================
# Dedicated private Azure Blob Storage account supporting physical verification
# and treatment evidence (weighbridge tickets, destruction certificates, photos).
# - Account Tier: Standard_LRS (lowest approved cost tier satisfying 11 9's durability).
# - Access Tier: Hot (optimized for active verification workflows).
# - Network: Zero public internet access; strictly isolated via Azure Private Link.
# - Authentication: Shared access keys enabled for IaC provider management; runtime
#   workload strictly authenticates via secretless Azure AD Managed Identity.
# ============================================================================

resource "azurerm_storage_account" "evidence" {
  # checkov:skip=CKV_AZURE_144:LRS replication approved in Decision D2 for single-region academic MVP cost control.
  # checkov:skip=CKV_AZURE_166:Customer Managed Keys (CMK) disabled for academic MVP cost control; uses Microsoft-managed keys.
  # checkov:skip=CKV2_AZURE_18:Customer Managed Keys (CMK) disabled for academic MVP cost control; uses Microsoft-managed keys.
  # checkov:skip=CKV2_AZURE_1:Customer Managed Keys (CMK) disabled for academic MVP cost control; uses Microsoft-managed keys.
  # checkov:skip=CKV_AZURE_206:Advanced Threat Protection disabled for academic MVP cost control; uses Microsoft-managed keys.
  # checkov:skip=CKV_AZURE_33:Storage logging for queues not applicable to blob-only evidence store.
  # checkov:skip=CKV_AZURE_3:Secure transfer is enabled via https_traffic_only_enabled=true and TLS 1.2+.
  # checkov:skip=CKV_AZURE_35:Public network access is disabled (Zero-Trust ADR D2) and network rules default to Deny.
  name                              = "stgewastescalable${var.environment}"
  resource_group_name               = azurerm_resource_group.env_rg.name
  location                          = azurerm_resource_group.env_rg.location
  account_tier                      = "Standard"
  account_replication_type          = "LRS"
  account_kind                      = "StorageV2"
  https_traffic_only_enabled        = true
  min_tls_version                   = "TLS1_2"
  public_network_access_enabled     = false # Strict zero-trust compliance (ADR D2)
  allow_nested_items_to_be_public   = false # Disallow anonymous public blob access
  shared_access_key_enabled         = false
  default_to_oauth_authentication   = true
  infrastructure_encryption_enabled = true

  network_rules {
    default_action = "Deny"
    bypass         = ["AzureServices"]
  }

  lifecycle {
    ignore_changes = [
      blob_properties,
      share_properties,
      queue_properties,
      static_website
    ]
  }

  tags = local.common_tags
}

# Manage the existing default Blob service without opening the private data plane.
resource "azapi_update_resource" "evidence_blob_protection" {
  type        = "Microsoft.Storage/storageAccounts/blobServices@2023-01-01"
  resource_id = "${azurerm_storage_account.evidence.id}/blobServices/default"
  body = {
    properties = {
      isVersioningEnabled = true
      deleteRetentionPolicy = {
        enabled = true
        days    = 7
      }
    }
  }
}

# Private Blob container dedicated to verification evidence.
# Provisioned through the ARM control plane (management.azure.com) rather than
# azurerm_storage_container, which calls the blob data plane
# (<account>.blob.core.windows.net). Because public_network_access_enabled is
# false, the data plane correctly rejects the GitHub-hosted runner with
# 403 AuthorizationFailure, so the container must be created via ARM.
resource "azapi_resource" "evidence_private" {
  type      = "Microsoft.Storage/storageAccounts/blobServices/containers@2023-01-01"
  name      = "evidence-private"
  parent_id = "${azurerm_storage_account.evidence.id}/blobServices/default"

  body = {
    properties = {
      publicAccess = "None"
    }
  }
  depends_on = [azapi_update_resource.evidence_blob_protection]
}

# ============================================================================
# PRIVATE LINK & PRIVATE DNS INTEGRATION
# ============================================================================

resource "azurerm_private_dns_zone" "blob_dns" {
  name                = "privatelink.blob.core.windows.net"
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "blob_dns_link" {
  name                  = "vnetlink-blob"
  private_dns_zone_name = azurerm_private_dns_zone.blob_dns.name
  virtual_network_id    = azurerm_virtual_network.vnet.id
  resource_group_name   = azurerm_resource_group.env_rg.name
}

resource "azurerm_private_endpoint" "storage_pe" {
  name                = "pe-stgewastescalable-${var.environment}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  subnet_id           = azurerm_subnet.private_endpoints_subnet.id
  tags                = local.common_tags

  private_service_connection {
    name                           = "psc-stgewastescalable-${var.environment}"
    private_connection_resource_id = azurerm_storage_account.evidence.id
    subresource_names              = ["blob"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "pdz-group-blob"
    private_dns_zone_ids = [azurerm_private_dns_zone.blob_dns.id]
  }
}

# ============================================================================
# RBAC: SECRETLESS AZURE AD MANAGED IDENTITY ACCESS
# ============================================================================

# Grant ACA API User-Assigned Managed Identity read/write access to evidence blobs
resource "azurerm_role_assignment" "storage_blob_data_contributor" {
  scope                = azurerm_storage_account.evidence.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.aca_identity.principal_id
}

# Grant Deployment Service Principal access for pipeline validation
resource "azurerm_role_assignment" "sp_storage_blob_data_contributor" {
  scope                = azurerm_storage_account.evidence.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = data.azurerm_client_config.current.object_id
}

# Grant Self-Hosted Runner VM access to Blob Storage when runner is enabled
resource "azurerm_role_assignment" "runner_storage_blob_data_contributor" {
  count                = var.enable_self_hosted_runner ? 1 : 0
  scope                = azurerm_storage_account.evidence.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.runner_identity[0].principal_id
}
