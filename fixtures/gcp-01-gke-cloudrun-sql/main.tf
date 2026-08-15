# Fixture gcp-01: the GCP shape of the same asymmetry.
#
# Shaped like terraform a model would generate from "give me a GKE cluster and a
# Cloud Run API on a postgres database behind Cloud NAT": every resource is
# correct in isolation, the plan applies cleanly, and the sizes never talk to
# each other.
#
# What it hides:
#   GC1  Cloud Run scales to 100 instances holding 10 connections each (1000)
#        against a db-custom-2-7680 postgres that accepts 400
#   GC2  the pod secondary range is a /21 and max_pods_per_node is 110, so GKE
#        carves a /24 per node and the range holds 8 nodes against a node pool
#        that autoscales to 30
#   GC3  one manually allocated NAT IP at 1024 ports per VM serves 63 VMs
#        against 30 nodes plus a managed instance group that reaches 60
#   GC4  a 20 GiB pd-standard disk sustains 15 read IOPS
#   GC5  an f1-micro serverless VPC connector capped at 3 instances carries
#        everything Cloud Run sends into the VPC
#   GC6  the Cloud SQL instance has disk_autoresize disabled at 20 GiB
#
# It plans without a Google Cloud account: the provider is given a fake OAuth
# access token so it never parses a service account key, and the fixture uses no
# data sources, so nothing reaches an API during plan.

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

provider "google" {
  project      = "headroom-fixture"
  region       = "us-central1"
  zone         = "us-central1-a"
  access_token = "mock-access-token"
}

resource "google_compute_network" "main" {
  name                    = "main"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "main" {
  name          = "main"
  region        = "us-central1"
  network       = google_compute_network.main.id
  ip_cidr_range = "10.0.0.0/24"

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = "10.4.0.0/21"
  }

  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = "10.8.0.0/24"
  }
}

resource "google_container_cluster" "main" {
  name                      = "main"
  location                  = "us-central1"
  network                   = google_compute_network.main.id
  subnetwork                = google_compute_subnetwork.main.id
  initial_node_count        = 1
  remove_default_node_pool  = true
  default_max_pods_per_node = 110
  deletion_protection       = false

  ip_allocation_policy {
    cluster_secondary_range_name  = "pods"
    services_secondary_range_name = "services"
  }
}

resource "google_container_node_pool" "main" {
  name     = "main"
  cluster  = google_container_cluster.main.id
  location = "us-central1"

  autoscaling {
    min_node_count = 3
    max_node_count = 30
  }

  node_config {
    machine_type = "e2-standard-4"
    disk_type    = "pd-balanced"
    disk_size_gb = 100
  }
}

resource "google_sql_database_instance" "main" {
  name                = "main"
  database_version    = "POSTGRES_16"
  region              = "us-central1"
  deletion_protection = false

  settings {
    tier              = "db-custom-2-7680"
    availability_type = "ZONAL"
    disk_size         = 20
    disk_type         = "PD_SSD"
    disk_autoresize   = false

    ip_configuration {
      ipv4_enabled    = false
      private_network = google_compute_network.main.id
    }
  }
}

resource "google_vpc_access_connector" "main" {
  name          = "main"
  region        = "us-central1"
  network       = google_compute_network.main.name
  ip_cidr_range = "10.9.0.0/28"
  machine_type  = "f1-micro"
  min_instances = 2
  max_instances = 3
}

resource "google_cloud_run_v2_service" "api" {
  name                = "api"
  location            = "us-central1"
  ingress             = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
  deletion_protection = false

  template {
    scaling {
      min_instance_count = 1
      max_instance_count = 100
    }

    vpc_access {
      connector = google_vpc_access_connector.main.id
      egress    = "PRIVATE_RANGES_ONLY"
    }

    containers {
      image = "us-docker.pkg.dev/cloudrun/container/hello"

      env {
        name  = "DB_HOST"
        value = google_sql_database_instance.main.private_ip_address
      }

      env {
        name  = "DB_POOL_SIZE"
        value = "10"
      }
    }
  }
}

resource "google_compute_router" "main" {
  name    = "main"
  region  = "us-central1"
  network = google_compute_network.main.id
}

resource "google_compute_address" "nat" {
  name         = "nat"
  region       = "us-central1"
  address_type = "EXTERNAL"
}

resource "google_compute_router_nat" "main" {
  name                               = "main"
  region                             = "us-central1"
  router                             = google_compute_router.main.name
  nat_ip_allocate_option             = "MANUAL_ONLY"
  nat_ips                            = [google_compute_address.nat.self_link]
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
  min_ports_per_vm                   = 1024
}

resource "google_compute_instance_template" "worker" {
  name         = "worker"
  machine_type = "e2-medium"

  disk {
    source_image = "debian-cloud/debian-12"
    disk_type    = "pd-standard"
    disk_size_gb = 20
    auto_delete  = true
    boot         = true
  }

  network_interface {
    network    = google_compute_network.main.id
    subnetwork = google_compute_subnetwork.main.id
  }
}

resource "google_compute_region_instance_group_manager" "worker" {
  name               = "worker"
  region             = "us-central1"
  base_instance_name = "worker"

  version {
    instance_template = google_compute_instance_template.worker.id
  }

  named_port {
    name = "http"
    port = 8080
  }
}

resource "google_compute_region_autoscaler" "worker" {
  name   = "worker"
  region = "us-central1"
  target = google_compute_region_instance_group_manager.worker.id

  autoscaling_policy {
    min_replicas    = 2
    max_replicas    = 60
    cooldown_period = 60

    cpu_utilization {
      target = 0.6
    }
  }
}

resource "google_compute_disk" "scratch" {
  name = "scratch"
  zone = "us-central1-a"
  type = "pd-standard"
  size = 20
}
