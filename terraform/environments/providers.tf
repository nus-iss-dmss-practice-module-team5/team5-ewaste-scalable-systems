terraform {
  required_version = ">= 1.8.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.100"
    }

    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }

    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }

    # ARM control-plane management for child resources (e.g. blob containers)
    # that azurerm 3.x would otherwise manage through the network-blocked
    # storage data plane.
    azapi = {
      source  = "Azure/azapi"
      version = "~> 2.0"
    }
  }

  backend "azurerm" {
    # Dynamically configured by GitHub Actions CD.
    # Example state keys: dev.tfstate, stg.tfstate, prod.tfstate
  }
}

provider "azurerm" {
  features {
    storage {
      data_plane_available = false
    }
  }
  storage_use_azuread = true
}

# Authenticates with the same ARM_* environment variables as azurerm.
provider "azapi" {}

locals {
  name_prefix = "ewastescalable-${var.environment}"

  # Identity issues user tokens; every API must validate the same issuer.
  auth_issuer = "ewaste-identity-access"

  common_tags = {
    Project     = "Responsible E-Waste Chain-of-Custody"
    Environment = var.environment
    ManagedBy   = "Terraform"
    Course      = "SWE5006"
    Sprint      = "Sprint-1"
  }
}
