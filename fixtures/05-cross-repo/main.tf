# Fixture 05: a service repository that owns nothing underneath it.
#
# This is what most application repositories actually look like. The VPC belongs
# to the networking repository, the route table and the NAT gateway belong to
# whoever built the egress path, and this plan only creates the thin layer on
# top. Read on its own, it is a handful of resources floating in nothing.
#
# It exercises both ways a plan can point outside itself:
#   - a terraform_remote_state block, which names another repository's state
#     even when no identifier resolves
#   - literal cloud identifiers, which resolve to the same hash in whichever
#     plan actually manages them
#
# The state file next to this config stands in for the networking repository.

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

data "terraform_remote_state" "network" {
  backend = "local"

  config = {
    path = "./network.tfstate"
  }
}

resource "aws_subnet" "app" {
  vpc_id            = data.terraform_remote_state.network.outputs.vpc_id
  cidr_block        = "10.7.4.0/26"
  availability_zone = "us-east-1a"
}

resource "aws_security_group" "app" {
  name   = "app"
  vpc_id = data.terraform_remote_state.network.outputs.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "app" {
  ami                    = "ami-0000000000000dead"
  instance_type          = "t3.medium"
  subnet_id              = aws_subnet.app.id
  vpc_security_group_ids = [aws_security_group.app.id]

  root_block_device {
    volume_type = "gp3"
    volume_size = 20
  }
}

# Egress belongs to another repository. Only the identifiers cross over.
resource "aws_route" "egress" {
  route_table_id         = "rtb-0aaaaaaaaaaaaaaa2"
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = "nat-0aaaaaaaaaaaaaaa3"
}
