# Fixture 02: the shape of a self-hosted stack on VMs.
#
# One instance per service, a data volume each, a single NAT gateway for the
# whole environment. This is what an observability or platform stack looks like
# when it is deployed on EC2 instead of containers, and it exercises rules that
# the ECS fixture never touches.
#
# What it hides:
#   R4  three subnets in three availability zones all route outbound traffic
#       through one NAT gateway that lives in a single zone
#   R5  20 GiB gp2 data volumes: 3000 IOPS while credits last, 100 after
#   R5  a gp3 volume asking for 8000 IOPS on 10 GiB, which AWS refuses outright

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
  region                      = "us-east-1"
  access_key                  = "mock_access_key"
  secret_key                  = "mock_secret_key"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  skip_region_validation      = true
}

locals {
  services = {
    prometheus = "us-east-1a"
    loki       = "us-east-1b"
    tempo      = "us-east-1c"
  }
}

resource "aws_vpc" "main" {
  cidr_block = "10.42.0.0/16"
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.42.0.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_subnet" "private" {
  for_each = local.services

  vpc_id            = aws_vpc.main.id
  cidr_block        = cidrsubnet(aws_vpc.main.cidr_block, 8, index(keys(local.services), each.key) + 10)
  availability_zone = each.value
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

# One gateway, in one zone, for everything.
resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public.id
  depends_on    = [aws_internet_gateway.main]
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.main.id
  }
}

resource "aws_route_table_association" "private" {
  for_each = aws_subnet.private

  subnet_id      = each.value.id
  route_table_id = aws_route_table.private.id
}

resource "aws_security_group" "node" {
  name   = "node"
  vpc_id = aws_vpc.main.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "node" {
  for_each = local.services

  ami                    = "ami-0123456789abcdef0"
  instance_type          = "t3.small"
  subnet_id              = aws_subnet.private[each.key].id
  vpc_security_group_ids = [aws_security_group.node.id]

  root_block_device {
    volume_type = "gp3"
    volume_size = 30
  }

  tags = { Service = each.key }
}

# Data volumes, sized for the data and not for the load it will take.
resource "aws_ebs_volume" "data" {
  for_each = local.services

  availability_zone = each.value
  size              = 20
  type              = "gp2"
}

resource "aws_volume_attachment" "data" {
  for_each = local.services

  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data[each.key].id
  instance_id = aws_instance.node[each.key].id
}

# Someone asked for fast, and picked numbers the volume cannot carry.
resource "aws_ebs_volume" "wal" {
  availability_zone = "us-east-1a"
  size              = 10
  type              = "gp3"
  iops              = 8000
  throughput        = 500
}
