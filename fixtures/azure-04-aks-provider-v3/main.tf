# Fixture azure-04: azure-01's cluster, planned with azurerm 3.x.
#
# Same topology, same numbers, one difference: the provider. azurerm 4.0
# renamed a pile of booleans, and enable_auto_scaling became
# auto_scaling_enabled. A rule that reads only the newer name sees false on a
# 3.x plan, falls back to node_count, and then states "node_count, no
# autoscaling" about a pool that is autoscaling. A separate pool that declares
# only min_count and max_count disappears from the analysis entirely.
#
# That is worse than going quiet: the tool asserts the opposite of what the
# plan says. This fixture exists so the two providers have to agree.
#
# Expected, identical to azure-01:
#   AZ2  (12 + 8) nodes, each taking one node address plus 110 pod addresses,
#        is 2,220 addresses against the 251 usable in a /24
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
      # Pinned exactly. The point of this fixture is the provider version, so
      # it must not drift to 4.x on somebody else's machine.
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

  # The 3.x spelling. In 4.x this became resource_provider_registrations.
  skip_provider_registration = true

  use_cli  = false
  use_oidc = false
}

resource "azurerm_resource_group" "main" {
  name     = "headroom-fixture-aks-v3"
  location = "eastus"
}

resource "azurerm_virtual_network" "main" {
  name                = "aks-v3"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  address_space       = ["10.0.0.0/16"]
}

resource "azurerm_subnet" "aks" {
  name                 = "aks"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_kubernetes_cluster" "main" {
  name                = "headroom-fixture-aks-v3"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  dns_prefix          = "headroom"

  default_node_pool {
    name           = "system"
    vm_size        = "Standard_D4s_v5"
    vnet_subnet_id = azurerm_subnet.aks.id
    # The 3.x spelling of the flag this whole fixture is about.
    enable_auto_scaling = true
    node_count          = 3
    min_count           = 3
    max_count           = 12
    max_pods            = 110
  }

  identity {
    type = "SystemAssigned"
  }

  # network_plugin_mode is not set, so this is Azure CNI node subnet mode and
  # every pod takes a real address out of azurerm_subnet.aks.
  network_profile {
    network_plugin = "azure"
    service_cidr   = "10.1.0.0/16"
    dns_service_ip = "10.1.0.10"
  }
}

# The pool that vanishes when the flag is misread: it never states node_count,
# so a reader that cannot see autoscaling has no number at all for it.
resource "azurerm_kubernetes_cluster_node_pool" "apps" {
  name                  = "apps"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.main.id
  vm_size               = "Standard_D4s_v5"
  vnet_subnet_id        = azurerm_subnet.aks.id
  enable_auto_scaling   = true
  min_count             = 2
  max_count             = 8
  max_pods              = 110
}
