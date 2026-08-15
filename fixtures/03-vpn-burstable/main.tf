# Fixture 03: a single-site hybrid VPC.
#
# Modelled on a real environment: one public subnet in one zone, one burstable
# instance, and two Site-to-Site VPN connections into a virtual private gateway
# for carrier redundancy. Nothing here is oversized, nothing is obviously wrong,
# and terraform validate has nothing to say about it.
#
# What it hides:
#   R7  a t3.xlarge sustains 1.6 of its 4 vCPUs, and with no credit_specification
#       it launches in unlimited mode, so load above baseline is billed rather
#       than throttled, with nothing in the plan bounding the amount
#   R8  two VPN connections on a VGW read as twice the pipe. A VGW does not do
#       ECMP, so the ceiling stays at one tunnel.

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region                      = "sa-east-1"
  access_key                  = "mock_access_key"
  secret_key                  = "mock_secret_key"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  skip_region_validation      = true
}

resource "aws_vpc" "main" {
  cidr_block           = "172.32.0.0/22"
  enable_dns_support   = true
  enable_dns_hostnames = true
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = "172.32.0.0/24"
  availability_zone       = "sa-east-1a"
  map_public_ip_on_launch = true
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "ec2" {
  name   = "app"
  vpc_id = aws_vpc.main.id

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["10.0.0.0/8"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "main" {
  ami                    = "ami-0000000000000dead"
  instance_type          = "t3.xlarge"
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.ec2.id]

  root_block_device {
    volume_type = "gp3"
    volume_size = 30
    encrypted   = true
  }
}

resource "aws_vpn_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_customer_gateway" "primary" {
  bgp_asn    = 65000
  ip_address = "198.51.100.10"
  type       = "ipsec.1"
}

resource "aws_customer_gateway" "secondary" {
  bgp_asn    = 65000
  ip_address = "198.51.100.20"
  type       = "ipsec.1"
}

# Two carriers, one gateway. Redundancy, not bandwidth.
resource "aws_vpn_connection" "primary" {
  vpn_gateway_id      = aws_vpn_gateway.main.id
  customer_gateway_id = aws_customer_gateway.primary.id
  type                = "ipsec.1"
  static_routes_only  = false
}

resource "aws_vpn_connection" "secondary" {
  vpn_gateway_id      = aws_vpn_gateway.main.id
  customer_gateway_id = aws_customer_gateway.secondary.id
  type                = "ipsec.1"
  static_routes_only  = false
}
