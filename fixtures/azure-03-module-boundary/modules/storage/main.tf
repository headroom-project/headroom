# The storage module. Three 1 TiB Premium SSD disks, which Azure bills and
# serves as P30: 5,000 IOPS and 200 MB/s each. Nothing in this module says what
# they will be attached to, which is the whole point: the size of the VM in
# front of them is decided in a different file by a different person.

variable "resource_group_name" { type = string }
variable "location" { type = string }

resource "azurerm_managed_disk" "app" {
  for_each = toset(["data", "logs", "temp"])

  name                 = "app-${each.key}"
  location             = var.location
  resource_group_name  = var.resource_group_name
  storage_account_type = "Premium_LRS"
  create_option        = "Empty"
  disk_size_gb         = 1024
}

# 128 GiB of Premium SSD is a P10: 500 IOPS and 100 MB/s, which the control VM
# drives comfortably.
resource "azurerm_managed_disk" "archive" {
  name                 = "archive-data"
  location             = var.location
  resource_group_name  = var.resource_group_name
  storage_account_type = "Premium_LRS"
  create_option        = "Empty"
  disk_size_gb         = 128
}

output "app_disk_ids" {
  value = { for k, d in azurerm_managed_disk.app : k => d.id }
}

output "archive_disk_id" {
  value = azurerm_managed_disk.archive.id
}
