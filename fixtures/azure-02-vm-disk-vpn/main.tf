# Fixture azure-02: hybrid connectivity and the storage tier.
#
# Shaped like terraform a model would generate from "give me a Linux VM with a
# 1 TB premium disk and a site-to-site VPN to two branch offices".
#
# What it hides:
#   AZ4  two IPsec connections terminate on one VpnGw1 gateway. All tunnels on
#        a VPN gateway share the SKU's aggregate throughput, so the second
#        connection buys a second site, never a second 650 Mbps
#   AZ5  a P30 disk provisions 5,000 IOPS and 200 MB/s onto a Standard_B2s,
#        which cannot drive more than 1,280 IOPS and 15 MB/s uncached
#   AZ6  Standard_B2s sustains 20% of its 2 vCPUs and then throttles. Azure
#        B-series has no equivalent of the T3 "unlimited" escape hatch
#
# Plans with no subscription the same way as azure-01:
#   python ../azure-01-aks-postgres/fake-imds.py &
#   export ARM_PROVIDER_ENHANCED_VALIDATION=false
#   terraform plan -out=tfplan

terraform {
  required_version = ">= 1.5"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.20"
    }
  }
}

provider "azurerm" {
  features {}

  subscription_id = "00000000-0000-0000-0000-000000000000"
  tenant_id       = "11111111-1111-1111-1111-111111111111"
  client_id       = "22222222-2222-2222-2222-222222222222"

  use_msi      = true
  msi_endpoint = "http://127.0.0.1:47712/metadata/identity/oauth2/token"

  resource_provider_registrations = "none"

  use_cli  = false
  use_oidc = false
}

resource "azurerm_resource_group" "main" {
  name     = "headroom-fixture-hybrid"
  location = "eastus"
}

resource "azurerm_virtual_network" "main" {
  name                = "hybrid"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  address_space       = ["10.10.0.0/16"]
}

resource "azurerm_subnet" "app" {
  name                 = "app"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.10.1.0/24"]
}

# A VPN gateway must live in a subnet named exactly GatewaySubnet.
resource "azurerm_subnet" "gateway" {
  name                 = "GatewaySubnet"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.10.255.0/27"]
}

###############################################################################
# Hybrid connectivity
###############################################################################

resource "azurerm_public_ip" "vpn" {
  name                = "vpn"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_virtual_network_gateway" "main" {
  name                = "main"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  type     = "Vpn"
  vpn_type = "RouteBased"
  sku      = "VpnGw1"

  ip_configuration {
    name                          = "default"
    public_ip_address_id          = azurerm_public_ip.vpn.id
    private_ip_address_allocation = "Dynamic"
    subnet_id                     = azurerm_subnet.gateway.id
  }
}

resource "azurerm_local_network_gateway" "branch_a" {
  name                = "branch-a"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  gateway_address     = "198.51.100.10"
  address_space       = ["192.168.10.0/24"]
}

resource "azurerm_local_network_gateway" "branch_b" {
  name                = "branch-b"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  gateway_address     = "198.51.100.20"
  address_space       = ["192.168.20.0/24"]
}

# shared_key is required by the resource. These are fixture values for
# infrastructure that does not exist and never will.
resource "azurerm_virtual_network_gateway_connection" "branch_a" {
  name                = "branch-a"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  type                       = "IPsec"
  virtual_network_gateway_id = azurerm_virtual_network_gateway.main.id
  local_network_gateway_id   = azurerm_local_network_gateway.branch_a.id
  shared_key                 = "fixture-only-not-a-real-psk-a"
}

resource "azurerm_virtual_network_gateway_connection" "branch_b" {
  name                = "branch-b"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  type                       = "IPsec"
  virtual_network_gateway_id = azurerm_virtual_network_gateway.main.id
  local_network_gateway_id   = azurerm_local_network_gateway.branch_b.id
  shared_key                 = "fixture-only-not-a-real-psk-b"
}

###############################################################################
# Compute and storage
###############################################################################

resource "azurerm_network_interface" "app" {
  name                = "app"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.app.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_linux_virtual_machine" "app" {
  name                = "app"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  # Cheap, and the cheapness is a CPU ceiling nothing in this file mentions.
  size                  = "Standard_B2s"
  admin_username        = "azureuser"
  network_interface_ids = [azurerm_network_interface.app.id]

  admin_ssh_key {
    username = "azureuser"
    # A throwaway public key generated for this fixture. Public keys are not
    # secrets, and the private half was never kept.
    public_key = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDGTBPHACN6k+U3pXimcDu6e1ByKWdnDVtQRE3wxbzuHVU2xieVyt1U78OCTUsXGGr5Zie9RqU98Zgoiay8OQH/Q9pISBVZW/cqdw2ivlYncCrduC641SJAhvs7YnuaT4AMspEGvQqC4loxGpDF5OTNN5FlyP1dTpQiaZoEacQ9B3vEqKokydL2IUopJyz0NmSdNRa2EI7PtXt/Y0jcbYQEYHv+m9NwDcB4aFrYcMAVG2T+MQaL3wxuQkcEzHOYV3pTQb30IFHCfbbsvgMguL+et/Fzn7cU3W88B/BhfYBYz8gQxaJVwiIrqgsTVDWB1+b56V9wvSWShWrUuIoBqXkt headroom-fixture"
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Premium_LRS"
    disk_size_gb         = 64
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "ubuntu-24_04-lts"
    sku       = "server"
    version   = "latest"
  }
}

# 1 TiB of Premium SSD is a P30: 5,000 IOPS and 200 MB/s provisioned and paid
# for. The VM in front of it is the ceiling.
resource "azurerm_managed_disk" "data" {
  name                 = "app-data"
  location             = azurerm_resource_group.main.location
  resource_group_name  = azurerm_resource_group.main.name
  storage_account_type = "Premium_LRS"
  create_option        = "Empty"
  disk_size_gb         = 1024
}

resource "azurerm_virtual_machine_data_disk_attachment" "data" {
  managed_disk_id    = azurerm_managed_disk.data.id
  virtual_machine_id = azurerm_linux_virtual_machine.app.id
  lun                = 0
  caching            = "None"
}
