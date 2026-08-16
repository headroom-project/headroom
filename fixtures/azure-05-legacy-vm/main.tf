# Fixture azure-05: the pre split virtual machine resource.
#
# azurerm_virtual_machine came before azurerm_linux_virtual_machine and
# azurerm_windows_virtual_machine, and azurerm 4.0 removed it. Estates built
# before that split still declare it by the hundred, and the resource is still
# planned every day by anyone pinned to 3.x.
#
# Nothing about the capacity question changes with the resource type, but the
# shape does: the size is vm_size rather than size, the os disk is
# storage_os_disk with managed_disk_type rather than os_disk with
# storage_account_type, and data disks can be declared inline as
# storage_data_disk instead of attached by a separate resource. A rule that
# iterates over the two newer type names walks straight past all of it.
#
# What it hides:
#   AZ5  two inline 1 TiB Premium SSD data disks are P30, so 10,000 IOPS and
#        400 MB/s uncached, in front of a Standard_D4s_v5 that drives 6,400
#        and 145
#
# Plans with no subscription the same way as azure-01:
#   python ../azure-01-aks-postgres/fake-imds.py &
#   export ARM_PROVIDER_ENHANCED_VALIDATION=false
#   terraform init && terraform plan -out=tfplan
#   terraform show -json tfplan > plan.json

terraform {
  required_version = ">= 1.5"
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
      # Pinned exactly: 4.x removed the resource this fixture is about.
      version = "3.71.0"
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

  skip_provider_registration = true

  use_cli  = false
  use_oidc = false
}

resource "azurerm_resource_group" "main" {
  name     = "headroom-fixture-legacy"
  location = "eastus"
}

resource "azurerm_virtual_network" "main" {
  name                = "legacy"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  address_space       = ["10.40.0.0/16"]
}

resource "azurerm_subnet" "app" {
  name                 = "app"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.40.1.0/24"]
}

resource "azurerm_network_interface" "db" {
  name                = "db"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.app.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_virtual_machine" "db" {
  name                  = "db"
  location              = azurerm_resource_group.main.location
  resource_group_name   = azurerm_resource_group.main.name
  network_interface_ids = [azurerm_network_interface.db.id]

  # The old spelling of size. 6,400 uncached IOPS and 145 MB/s.
  vm_size = "Standard_D4s_v5"

  delete_os_disk_on_termination    = true
  delete_data_disks_on_termination = true

  storage_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts"
    version   = "latest"
  }

  # The old spelling of the os disk, with managed_disk_type where the newer
  # resources say storage_account_type.
  storage_os_disk {
    name              = "db-os"
    caching           = "ReadWrite"
    create_option     = "FromImage"
    managed_disk_type = "Premium_LRS"
    disk_size_gb      = 64
  }

  # Data disks declared inline, which the newer resources cannot do at all.
  # 1 TiB of Premium SSD is a P30: 5,000 IOPS and 200 MB/s each.
  storage_data_disk {
    name              = "db-data"
    lun               = 0
    caching           = "None"
    create_option     = "Empty"
    managed_disk_type = "Premium_LRS"
    disk_size_gb      = 1024
  }

  storage_data_disk {
    name              = "db-logs"
    lun               = 1
    caching           = "None"
    create_option     = "Empty"
    managed_disk_type = "Premium_LRS"
    disk_size_gb      = 1024
  }

  os_profile {
    computer_name  = "db"
    admin_username = "azureuser"
  }

  os_profile_linux_config {
    disable_password_authentication = true

    ssh_keys {
      path = "/home/azureuser/.ssh/authorized_keys"
      # A throwaway public key generated for this fixture. Public keys are not
      # secrets, and the private half was never kept.
      key_data = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDGTBPHACN6k+U3pXimcDu6e1ByKWdnDVtQRE3wxbzuHVU2xieVyt1U78OCTUsXGGr5Zie9RqU98Zgoiay8OQH/Q9pISBVZW/cqdw2ivlYncCrduC641SJAhvs7YnuaT4AMspEGvQqC4loxGpDF5OTNN5FlyP1dTpQiaZoEacQ9B3vEqKokydL2IUopJyz0NmSdNRa2EI7PtXt/Y0jcbYQEYHv+m9NwDcB4aFrYcMAVG2T+MQaL3wxuQkcEzHOYV3pTQb30IFHCfbbsvgMguL+et/Fzn7cU3W88B/BhfYBYz8gQxaJVwiIrqgsTVDWB1+b56V9wvSWShWrUuIoBqXkt headroom-fixture"
    }
  }
}
