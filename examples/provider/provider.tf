terraform {
  required_version = ">= 1.13.0"

  required_providers {
    cantora = {
      source  = "cantora/cantora"
      version = "~> 0.1"
    }
  }
}

provider "cantora" {
  # Set CANTORA_API_KEY in the environment.
  request_timeout   = "30s"
  source_repository = "acme/agent-configuration"
  source_commit     = var.git_commit
  source_path       = "agents/aria.tf"
  source_workflow   = "terraform-apply"
  source_run        = var.workflow_run
  source_actor      = var.workflow_actor
}

variable "git_commit" {
  type = string
}

variable "workflow_run" {
  type = string
}

variable "workflow_actor" {
  type = string
}
