variable "environment" {
  type        = string
  default     = "dev"
  description = "Target deployment environment: dev, stg, or prod."

  validation {
    condition     = contains(["dev", "stg", "prod"], var.environment)
    error_message = "environment must be one of: dev, stg, prod."
  }
}

variable "location" {
  type        = string
  default     = "malaysiawest"
  description = "Primary Azure region."
}

variable "tenant_id" {
  type        = string
  description = "Microsoft Entra ID tenant GUID."
}

variable "shared_rg_name" {
  type        = string
  default     = "rg-ewastescalable-shared"
  description = "Name of the shared persistent resource group."
}

variable "shared_acr_name" {
  type        = string
  default     = "acrewastescalable"
  description = "Name of the shared Azure Container Registry."
}

variable "shared_log_analytics_name" {
  type        = string
  default     = "log-ewastescalable-centralized"
  description = "Name of the shared Log Analytics Workspace."
}

variable "db_admin_username" {
  type        = string
  default     = "ewasteadmin"
  description = "Administrator login for MySQL Flexible Server."
}

variable "db_admin_password" {
  type        = string
  sensitive   = true
  description = "Administrator password for MySQL Flexible Server."
}

# ============================================================================
# CONTAINER IMAGES (DECOUPLED MICROSERVICES)
# ============================================================================
variable "image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Default/fallback image digest from ACR, or initial bootstrap image."
}

variable "ui_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable Next.js frontend image digest from ACR, or initial bootstrap image."
}

variable "auth_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable identity-access-svc image digest from ACR, or bootstrap placeholder."
}

variable "batch_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable batch-workflow-svc image digest from ACR, or bootstrap placeholder."
}

variable "claim_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable claim-logistics-svc image digest from ACR, or bootstrap placeholder."
}

variable "custody_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable custody-verification-svc image digest from ACR, or bootstrap placeholder."
}

variable "matching_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable matching-worker-svc image digest from ACR, or bootstrap placeholder."
}

variable "analytics_image_digest" {
  type        = string
  default     = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
  description = "Immutable analytics-worker-svc image digest from ACR, or bootstrap placeholder."
}

# ============================================================================
# REPLICA OVERRIDES (SUPPORTING CAPACITY SCALE RUNS)
# ============================================================================
variable "batch_min_replicas" {
  type        = number
  default     = 1
  description = "Minimum replicas for batch-workflow-svc (lock to 3 for R2 scaling run)."
}

variable "batch_max_replicas" {
  type        = number
  default     = 3
  description = "Maximum replicas for batch-workflow-svc (lock to 1 for R1 capacity screen)."
}


variable "auth_access_secret" {
  type        = string
  sensitive   = true
  description = "JWT access token signing secret for backend API."
}

variable "auth_refresh_secret" {
  type        = string
  sensitive   = true
  description = "JWT refresh token signing secret for backend API."
}

variable "auth_refresh_hash_secret" {
  type        = string
  sensitive   = true
  description = "HMAC secret for refresh token fingerprinting."
}

variable "matcher_signing_secret" {
  type        = string
  sensitive   = true
  description = "Dedicated matcher workload signing key; must match CD's MATCHER_SIGNING_SECRET."

  validation {
    condition     = length(var.matcher_signing_secret) >= 32
    error_message = "matcher_signing_secret must contain at least 32 characters."
  }
}

variable "analytics_service_token" {
  type        = string
  sensitive   = true
  description = "Required shared service bearer token for internal communication between the Analytics worker and the backend API; inject it through a secret or an untracked .tfvars file."

  validation {
    condition     = length(var.analytics_service_token) >= 32
    error_message = "analytics_service_token must contain at least 32 characters."
  }
}

variable "github_repository" {
  type        = string
  default     = "nus-iss-dmss-practice-module-team5/team5-ewaste-scalable-systems"
  description = "Target GitHub repository in 'owner/repo' format for self-hosted runner registration."
}

variable "github_pat" {
  type        = string
  sensitive   = true
  default     = ""
  description = "GitHub Personal Access Token (PAT) with 'repo' scope for runner registration."
}

variable "runner_vm_size" {
  type        = string
  default     = "Standard_B2als_v2"
  description = "Azure VM SKU for the VNet self-hosted GitHub runner."
}

variable "enable_self_hosted_runner" {
  type        = bool
  default     = true
  description = "Whether to provision the VNet-injected self-hosted GitHub runner VM."
}
