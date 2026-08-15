# Fixture azure-01: the canonical Azure scale asymmetry.
#
# Shaped like terraform a model would generate from "give me an AKS cluster and
# a web app with a postgres database on Azure": every resource is correct in
# isolation, the plan applies cleanly, and the sizes never talk to each other.
#
# What it hides:
#   AZ1  the App Service plan autoscales to 30 workers holding 20 connections
#        each (600) against a B_Standard_B1ms flexible server, which is created
#        with max_connections = 50 and keeps 15 of them for itself
#   AZ2  the AKS node subnet is a /24 (251 usable), and Azure CNI in node
#        subnet mode takes one address per *pod*, reserved up front per node:
#        two pools reaching 20 nodes x (1 + 110) = 2220 addresses
#   AZ3  one NAT gateway with one public IP (64,512 SNAT ports) fronts all
#        2200 of those pods, which is 29 ports each
#
# It plans without an Azure subscription. azurerm is harder to fool than the
# AWS provider, which has skip_credentials_validation; three things are needed:
#
#   python fake-imds.py &                            # a token that parses
#   export ARM_PROVIDER_ENHANCED_VALIDATION=false    # no ARM location lookup
#   terraform plan -out=tfplan                       # + registrations = none
#
# There are no data sources here, because every azurerm data source is a live
# API call and would defeat all of the above.

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

  # azurerm has no skip_credentials_validation: it acquires a token at
  # configure time and reads the tenant out of the claims before it will plan.
  # fake-imds.py serves one. Nothing here calls the ARM API afterwards.
  use_msi      = true
  msi_endpoint = "http://127.0.0.1:47712/metadata/identity/oauth2/token"

  # Stops the registration calls. It is not sufficient on its own: the provider
  # also populates a resource-provider cache from ARM for location validation,
  # and only ARM_PROVIDER_ENHANCED_VALIDATION=false suppresses that. Both are
  # needed to plan without a subscription.
  resource_provider_registrations = "none"

  use_cli  = false
  use_oidc = false
}

resource "azurerm_resource_group" "main" {
  name     = "headroom-fixture"
  location = "eastus"
}

resource "azurerm_virtual_network" "main" {
  name                = "main"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  address_space       = ["10.0.0.0/16"]
}

# A /24 looks generous for 12 nodes. With Azure CNI in node subnet mode it is
# not the nodes that consume addresses, it is the pods.
resource "azurerm_subnet" "aks" {
  name                 = "aks"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_subnet" "db" {
  name                 = "db"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.2.0/28"]

  delegation {
    name = "postgres"
    service_delegation {
      name    = "Microsoft.DBforPostgreSQL/flexibleServers"
      actions = ["Microsoft.Network/virtualNetworks/subnets/join/action"]
    }
  }
}

###############################################################################
# Egress: one NAT gateway, one public IP, both subnets behind it.
###############################################################################

resource "azurerm_public_ip" "nat" {
  name                = "nat"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_nat_gateway" "main" {
  name                = "main"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  sku_name            = "Standard"
}

resource "azurerm_nat_gateway_public_ip_association" "main" {
  nat_gateway_id       = azurerm_nat_gateway.main.id
  public_ip_address_id = azurerm_public_ip.nat.id
}

resource "azurerm_subnet_nat_gateway_association" "aks" {
  subnet_id      = azurerm_subnet.aks.id
  nat_gateway_id = azurerm_nat_gateway.main.id
}

###############################################################################
# Database
###############################################################################

resource "azurerm_private_dns_zone" "postgres" {
  name                = "headroom.postgres.database.azure.com"
  resource_group_name = azurerm_resource_group.main.name
}

resource "azurerm_postgresql_flexible_server" "main" {
  name                = "headroom-fixture-pg"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  version             = "16"

  # The smallest burstable tier, which is what a generator reaches for.
  sku_name   = "B_Standard_B1ms"
  storage_mb = 32768
  zone       = "1"

  delegated_subnet_id = azurerm_subnet.db.id
  private_dns_zone_id = azurerm_private_dns_zone.postgres.id

  # Entra-only auth keeps a password out of this fixture and out of plan.json.
  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = false
    tenant_id                     = "11111111-1111-1111-1111-111111111111"
  }
}

###############################################################################
# The application tier that talks to it
###############################################################################

resource "azurerm_service_plan" "api" {
  name                = "api"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  os_type             = "Linux"
  sku_name            = "P1v3"
  worker_count        = 2
}

resource "azurerm_linux_web_app" "api" {
  name                = "headroom-fixture-api"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_service_plan.api.location
  service_plan_id     = azurerm_service_plan.api.id

  site_config {}

  # Built from .name rather than .fqdn on purpose. fqdn is unknown until apply,
  # and one unknown value makes terraform mark the whole map unknown, which
  # would hide DB_POOL_SIZE from the plan as well.
  app_settings = {
    PGHOST       = "${azurerm_postgresql_flexible_server.main.name}.postgres.database.azure.com"
    DB_POOL_SIZE = "20"
    NODE_ENV     = "production"
  }
}

resource "azurerm_monitor_autoscale_setting" "api" {
  name                = "api"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  target_resource_id  = azurerm_service_plan.api.id

  profile {
    name = "default"

    capacity {
      default = 2
      minimum = 2
      maximum = 30
    }

    rule {
      metric_trigger {
        metric_name        = "CpuPercentage"
        metric_resource_id = azurerm_service_plan.api.id
        time_grain         = "PT1M"
        statistic          = "Average"
        time_window        = "PT5M"
        time_aggregation   = "Average"
        operator           = "GreaterThan"
        threshold          = 70
      }

      scale_action {
        direction = "Increase"
        type      = "ChangeCount"
        value     = "2"
        cooldown  = "PT5M"
      }
    }
  }
}

###############################################################################
# AKS
###############################################################################

resource "azurerm_kubernetes_cluster" "main" {
  name                = "headroom-fixture-aks"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  dns_prefix          = "headroom"

  default_node_pool {
    name                 = "system"
    vm_size              = "Standard_D4s_v5"
    vnet_subnet_id       = azurerm_subnet.aks.id
    auto_scaling_enabled = true
    node_count           = 3
    min_count            = 3
    max_count            = 12
    max_pods             = 110
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
    outbound_type  = "userAssignedNATGateway"
  }
}

# A second node pool in the same subnet. Nothing in the terraform adds the two
# pools together, but the subnet does.
resource "azurerm_kubernetes_cluster_node_pool" "apps" {
  name                  = "apps"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.main.id
  vm_size               = "Standard_D4s_v5"
  vnet_subnet_id        = azurerm_subnet.aks.id
  auto_scaling_enabled  = true
  node_count            = 2
  min_count             = 2
  max_count             = 8
  max_pods              = 110
}
