# Fixture 04: a queue, its consumer, and a database behind an autoscaling tier.
#
# Everything here applies cleanly and every individual number looks defensible.
# The defects live in the relationships between them.
#
# What it hides:
#   R3  a 30s visibility timeout in front of a 60s function timeout, so a
#       message is redelivered while the first invocation is still running it
#   R3  reserved concurrency of 2 on an SQS-triggered function, below the 5
#       Lambda needs to run the poller at all
#   R3  that same cap is a drain rate of 0.3 messages per second, which nobody
#       wrote down because it was typed as a concurrency
#   R6  the service is allowed to grow 20x in front of a database that cannot
#       grow at all
#   R6  allocated_storage with no max_allocated_storage, so storage autoscaling
#       is off and a full volume stops writes

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

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "private_a" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.1.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_subnet" "private_b" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.2.0/24"
  availability_zone = "us-east-1b"
}

# ---------------------------------------------------------------- queue side

resource "aws_sqs_queue" "orders" {
  name                       = "orders"
  visibility_timeout_seconds = 30
}

resource "aws_iam_role" "worker" {
  name = "worker"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

resource "aws_lambda_function" "worker" {
  function_name = "worker"
  role          = aws_iam_role.worker.arn
  handler       = "index.handler"
  runtime       = "nodejs22.x"
  filename      = "${path.module}/lambda.zip"

  timeout                        = 60
  memory_size                    = 512
  reserved_concurrent_executions = 2
}

resource "aws_lambda_event_source_mapping" "orders" {
  event_source_arn = aws_sqs_queue.orders.arn
  function_name    = aws_lambda_function.worker.arn
  batch_size       = 10
}

# ------------------------------------------------------------ request side

resource "aws_security_group" "app" {
  name   = "app"
  vpc_id = aws_vpc.main.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "db" {
  name   = "db"
  vpc_id = aws_vpc.main.id

  ingress {
    from_port       = 3306
    to_port         = 3306
    protocol        = "tcp"
    security_groups = [aws_security_group.app.id]
  }
}

resource "aws_db_subnet_group" "main" {
  name       = "main"
  subnet_ids = [aws_subnet.private_a.id, aws_subnet.private_b.id]
}

resource "aws_db_instance" "main" {
  identifier                  = "main"
  engine                      = "mysql"
  engine_version              = "8.0"
  instance_class              = "db.m5.large"
  allocated_storage           = 100
  username                    = "app"
  manage_master_user_password = true
  db_subnet_group_name        = aws_db_subnet_group.main.name
  vpc_security_group_ids      = [aws_security_group.db.id]
  skip_final_snapshot         = true
}

resource "aws_ecs_cluster" "main" {
  name = "main"
}

resource "aws_ecs_task_definition" "api" {
  family                   = "api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "512"
  memory                   = "1024"

  container_definitions = jsonencode([
    {
      name      = "api"
      image     = "public.ecr.aws/docker/library/node:22-alpine"
      essential = true
      environment = [
        { name = "DB_POOL_SIZE", value = "5" },
      ]
    }
  ])
}

resource "aws_ecs_service" "api" {
  name            = "api"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.api.arn
  desired_count   = 2
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = [aws_subnet.private_a.id, aws_subnet.private_b.id]
    security_groups = [aws_security_group.app.id]
  }
}

resource "aws_appautoscaling_target" "api" {
  service_namespace  = "ecs"
  resource_id        = "service/${aws_ecs_cluster.main.name}/${aws_ecs_service.api.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  min_capacity       = 2
  max_capacity       = 40
}
