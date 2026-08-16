# The compute module. It declares virtual machines and knows nothing about
# storage: the disks that decide whether these sizes are the right ones are
# declared in a sibling module and attached from the root.

variable "resource_group_name" { type = string }
variable "location" { type = string }
variable "subnet_id" { type = string }

resource "azurerm_network_interface" "app" {
  name                = "app"
  location            = var.location
  resource_group_name = var.resource_group_name

  ip_configuration {
    name                          = "internal"
    subnet_id                     = var.subnet_id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_network_interface" "archive" {
  name                = "archive"
  location            = var.location
  resource_group_name = var.resource_group_name

  ip_configuration {
    name                          = "internal"
    subnet_id                     = var.subnet_id
    private_ip_address_allocation = "Dynamic"
  }
}

# 4 vCPUs, and a storage ceiling of 6,400 uncached IOPS and 145 MB/s that
# nothing in this module mentions.
resource "azurerm_linux_virtual_machine" "app" {
  name                = "app"
  location            = var.location
  resource_group_name = var.resource_group_name

  size                  = "Standard_D4s_v5"
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

# The control VM: 12,800 uncached IOPS and 290 MB/s, in front of a single P10.
resource "azurerm_linux_virtual_machine" "archive" {
  name                = "archive"
  location            = var.location
  resource_group_name = var.resource_group_name

  size                  = "Standard_D8s_v5"
  admin_username        = "azureuser"
  network_interface_ids = [azurerm_network_interface.archive.id]

  admin_ssh_key {
    username   = "azureuser"
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

output "app_vm_id" {
  value = azurerm_linux_virtual_machine.app.id
}

output "archive_vm_id" {
  value = azurerm_linux_virtual_machine.archive.id
}
