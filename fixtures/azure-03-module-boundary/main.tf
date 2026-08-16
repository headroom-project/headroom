# Fixture azure-03: the same infrastructure, split across modules.
#
# Every other fixture in this directory is flat, and that is exactly how a
# whole class of defect survived 199 green tests. Real terraform puts the VM in
# one module, the disks in another, and the attachment that ties them together
# in the root, where it reaches both through module outputs. When the reference
# graph cannot follow an output out of a module, every rule that reasons about a
# relationship between two resources goes quiet, and the report says nothing at
# all rather than saying it could not tell.
#
# What it hides:
#   AZ5  three P30 disks put 15,000 IOPS and 600 MB/s in front of a
#        Standard_D4s_v5, which cannot drive more than 6,400 IOPS and 145 MB/s
#        uncached. The VM is declared in module.compute, the disks in
#        module.storage, and the attachments in the root module here.
#
# What it must NOT report, which matters just as much:
#   the archive VM is a Standard_D8s_v5 (12,800 IOPS, 290 MB/s) with a single
#   P10 (500 IOPS, 100 MB/s) attached the same way, across the same boundary.
#   A fix that makes the graph cross module outputs must not also make every
#   crossing into a finding.
#
# Plans with no subscription the same way as azure-01 and azure-02:
#   python ../azure-01-aks-postgres/fake-imds.py &
#   export ARM_PROVIDER_ENHANCED_VALIDATION=false
#   terraform init && terraform plan -out=tfplan
#   terraform show -json tfplan > plan.json

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
  name     = "headroom-fixture-modules"
  location = "eastus"
}

resource "azurerm_virtual_network" "main" {
  name                = "modules"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  address_space       = ["10.30.0.0/16"]
}

resource "azurerm_subnet" "app" {
  name                 = "app"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.30.1.0/24"]
}

# The VMs live here, and nothing in this module knows a disk exists.
module "compute" {
  source = "./modules/compute"

  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  subnet_id           = azurerm_subnet.app.id
}

# The disks live here, and nothing in this module knows a VM exists.
module "storage" {
  source = "./modules/storage"

  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
}

# The root module is the only place that knows both, and it knows them only
# through outputs. This is the edge the graph has to cross.
resource "azurerm_virtual_machine_data_disk_attachment" "app" {
  for_each = module.storage.app_disk_ids

  managed_disk_id    = each.value
  virtual_machine_id = module.compute.app_vm_id
  lun                = index(sort(keys(module.storage.app_disk_ids)), each.key)
  caching            = "None"
}

# The control. Same shape, same boundary, a VM that can drive what is attached.
resource "azurerm_virtual_machine_data_disk_attachment" "archive" {
  managed_disk_id    = module.storage.archive_disk_id
  virtual_machine_id = module.compute.archive_vm_id
  lun                = 0
  caching            = "None"
}
