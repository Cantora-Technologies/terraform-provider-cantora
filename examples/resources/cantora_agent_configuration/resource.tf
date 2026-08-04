resource "cantora_agent_configuration" "aria" {
  organization_id = var.cantora_organization_id
  project_id      = var.cantora_project_id
  key             = "aria"
  display_name    = "Aria"

  manifest {
    schema_version              = "config.cantora.ai/agent-version/v1alpha1"
    instructions                = file("${path.module}/agents/aria.md")
    classifier_model_release_id = var.classifier_model_release_id
    worker_model_release_id     = var.worker_model_release_id
    tools                       = ["google_drive", "run_code"]
    required_bindings           = []

    budgets {
      max_model_steps           = 12
      max_tool_calls            = 8
      total_wall_time_ms        = 60000
      model_step_time_ms        = 30000
      stream_stall_time_ms      = 10000
      max_input_tokens          = 100000
      max_output_tokens         = 10000
      max_reasoning_tokens      = 5000
      max_total_tokens          = 110000
      max_tool_result_bytes     = 1000000
      max_estimated_cost_micros = 500000
    }
  }

  test_release {
    environment_id = var.cantora_test_environment_id
    reason         = "Apply the reviewed Terraform configuration"
  }
}

variable "cantora_organization_id" {
  type = string
}

variable "cantora_project_id" {
  type = string
}

variable "cantora_test_environment_id" {
  type = string
}

variable "classifier_model_release_id" {
  type = string
}

variable "worker_model_release_id" {
  type = string
}
