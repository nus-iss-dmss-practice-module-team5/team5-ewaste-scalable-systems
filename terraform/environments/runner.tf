# ============================================================================
# ENTERPRISE CI/CD: VNET-INJECTED SELF-HOSTED GITHUB RUNNER
# ============================================================================
# Solves ACR Private Link isolation:
# - ACR public network access is completely disabled.
# - The runner VM is injected directly into vnet-ewastescalable-${var.environment} (10.0.4.0/24).
# - Resolves acrewastescalable.azurecr.io via privatelink.azurecr.io to 10.0.3.x.
# - Outbound long-polling connection to GitHub Actions (no inbound ports open).
# - Native Azure RBAC (AcrPush / AcrPull) via User-Assigned Managed Identity.
# ============================================================================

resource "azurerm_subnet" "runner_subnet" {
  count                = var.enable_self_hosted_runner ? 1 : 0
  name                 = "snet-runner"
  resource_group_name  = azurerm_resource_group.env_rg.name
  virtual_network_name = azurerm_virtual_network.vnet.name
  address_prefixes     = ["10.0.4.0/24"]
}

resource "azurerm_public_ip" "runner_pip" {
  # checkov:skip=CKV_AZURE_206:Self-hosted runner requires outbound connectivity to GitHub Actions control plane; all inbound traffic is strictly blocked by NSG DenyAllInboundInternet.
  count               = var.enable_self_hosted_runner ? 1 : 0
  name                = "pip-runner-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  allocation_method   = "Static"
  sku                 = "Standard"
  tags                = local.common_tags
}

resource "azurerm_network_security_group" "runner_nsg" {
  count               = var.enable_self_hosted_runner ? 1 : 0
  name                = "nsg-runner-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags

  # Absolute Zero Inbound Attack Surface:
  # The GitHub runner communicates via outbound long-polling only.
  security_rule {
    name                       = "DenyAllInboundInternet"
    priority                   = 1000
    direction                  = "Inbound"
    access                     = "Deny"
    protocol                   = "*"
    source_port_range          = "*"
    destination_port_range     = "*"
    source_address_prefix      = "Internet"
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "AllowOutboundInternetHTTPS"
    priority                   = 100
    direction                  = "Outbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "443"
    source_address_prefix      = "*"
    destination_address_prefix = "*"
  }

    security_rule {
    name                       = "AllowOutboundVNetMySQL"
    priority                   = 110
    direction                  = "Outbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "3306"
    source_address_prefix      = "*"
    destination_address_prefix = "VirtualNetwork"
  }
}

resource "azurerm_subnet_network_security_group_association" "runner_nsg_assoc" {
  count                     = var.enable_self_hosted_runner ? 1 : 0
  subnet_id                 = azurerm_subnet.runner_subnet[0].id
  network_security_group_id = azurerm_network_security_group.runner_nsg[0].id
}

resource "azurerm_user_assigned_identity" "runner_identity" {
  count               = var.enable_self_hosted_runner ? 1 : 0
  name                = "id-runner-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags
}

# Grant runner VM native passwordless push & pull access to ACR via Azure RBAC
resource "azurerm_role_assignment" "runner_acr_push" {
  count                = var.enable_self_hosted_runner ? 1 : 0
  scope                = data.azurerm_container_registry.shared_acr.id
  role_definition_name = "AcrPush"
  principal_id         = azurerm_user_assigned_identity.runner_identity[0].principal_id
}

resource "azurerm_role_assignment" "runner_acr_pull" {
  count                = var.enable_self_hosted_runner ? 1 : 0
  scope                = data.azurerm_container_registry.shared_acr.id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_user_assigned_identity.runner_identity[0].principal_id
}

resource "azurerm_network_interface" "runner_nic" {
  # checkov:skip=CKV_AZURE_119:Runner NIC uses public IP solely for outbound long-poll connection to GitHub Actions; all inbound traffic is strictly dropped by NSG DenyAllInboundInternet.
  count               = var.enable_self_hosted_runner ? 1 : 0
  name                = "nic-runner-${local.name_prefix}"
  location            = azurerm_resource_group.env_rg.location
  resource_group_name = azurerm_resource_group.env_rg.name
  tags                = local.common_tags

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.runner_subnet[0].id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.runner_pip[0].id
  }
}

resource "tls_private_key" "runner_ssh" {
  count     = var.enable_self_hosted_runner ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "azurerm_linux_virtual_machine" "runner" {
  # checkov:skip=CKV_AZURE_1:Password authentication is strictly disabled; uses 4096-bit RSA SSH key.
  # checkov:skip=CKV_AZURE_149:Password authentication disabled.
  # checkov:skip=CKV_AZURE_50:Extensions not used.
  count                           = var.enable_self_hosted_runner && var.github_pat != "" ? 1 : 0
  name                            = "vm-runner-${local.name_prefix}"
  location                        = azurerm_resource_group.env_rg.location
  resource_group_name             = azurerm_resource_group.env_rg.name
  network_interface_ids           = [azurerm_network_interface.runner_nic[0].id]
  size                            = var.runner_vm_size
  admin_username                  = "azureuser"
  disable_password_authentication = true

  admin_ssh_key {
    username   = "azureuser"
    public_key = tls_private_key.runner_ssh[0].public_key_openssh
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "StandardSSD_LRS"
    disk_size_gb         = 64
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts-gen2"
    version   = "latest"
  }

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.runner_identity[0].id]
  }

  custom_data = base64encode(templatefile("${path.module}/scripts/runner-init.sh", {
    GITHUB_PAT  = var.github_pat
    GITHUB_REPO = var.github_repository
    NAME_PREFIX = local.name_prefix
    ENV_NAME    = var.environment
  }))

  tags = local.common_tags
}
