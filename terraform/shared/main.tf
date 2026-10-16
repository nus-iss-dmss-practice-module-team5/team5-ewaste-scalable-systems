terraform {
  required_version = ">= 1.8.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.100"
    }
  }

  backend "azurerm" {
    # Dynamically configured by CI/CD.
    # Recommended state key for this tier: shared.tfstate
  }
}

provider "azurerm" {
  features {}
}

variable "acr_name" {
  type        = string
  default     = "acrewastescale"
  description = "Globally unique name for the shared Azure Container Registry."
}

variable "location" {
  type        = string
  default     = "indonesiacentral"
  description = "Primary Azure region for shared infrastructure."
}

# Persistent shared resource group.
resource "azurerm_resource_group" "shared" {
  name     = "rg-ewaste-shared"
  location = var.location

  tags = {
    Project   = "Responsible E-Waste Chain-of-Custody"
    ManagedBy = "Terraform"
    Tier      = "Shared"
    Course    = "SWE5006"
  }
}

# Centralized Azure Container Registry.
# Premium SKU required for Private Link. Public network access is strictly disabled.
# All image pushes from CI/CD runners and pulls from ACA runtime flow exclusively
# through per-environment Private Endpoints over Azure Private Link.
resource "azurerm_container_registry" "acr" {
  name                          = var.acr_name
  resource_group_name           = azurerm_resource_group.shared.name
  location                      = azurerm_resource_group.shared.location
  sku                           = "Premium"
  admin_enabled                 = false
  anonymous_pull_enabled        = false
  public_network_access_enabled = false
  network_rule_bypass_option    = "AzureServices"

  tags = {
    Project = "Responsible E-Waste Chain-of-Custody"
    Tier    = "Shared"
  }
}

# Centralized Log Analytics Workspace for Azure Monitor telemetry.
resource "azurerm_log_analytics_workspace" "logs" {
  name                = "log-ewaste-centralized"
  resource_group_name = azurerm_resource_group.shared.name
  location            = azurerm_resource_group.shared.location
  sku                 = "PerGB2018"
  retention_in_days   = 30

  tags = {
    Project = "Responsible E-Waste Chain-of-Custody"
    Tier    = "Shared"
  }
}

output "acr_id" {
  value       = azurerm_container_registry.acr.id
  description = "Resource ID of the centralized Azure Container Registry."
}

output "acr_login_server" {
  value       = azurerm_container_registry.acr.login_server
  description = "Login server endpoint for the Azure Container Registry."
}

output "log_analytics_workspace_id" {
  value       = azurerm_log_analytics_workspace.logs.id
  description = "Resource ID of the centralized Log Analytics Workspace."
}
