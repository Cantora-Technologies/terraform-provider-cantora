# Terraform Provider for Cantora

The Cantora provider manages stable [Agent Configuration](https://cantora.ai/your-first-agent) through Cantora's Management API. Terraform owns the desired configuration and remote Agent Definition identity; Cantora retains immutable Agent Versions, append-only Agent Releases, and Configuration Revision evidence beneath it.

The initial provider exposes one resource: `cantora_agent_configuration`. It supports side-effect-free planning, saved-plan preconditions, idempotent apply recovery, ordinary drift detection, explicit import, and non-destructive removal from Terraform state.

## Use the provider

```hcl
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
  source_repository = "acme/agent-configuration"
  source_commit     = var.git_commit
  source_path       = "agents/aria.tf"
  source_workflow   = "terraform-apply"
  source_run        = var.workflow_run
  source_actor      = var.workflow_actor
}
```

Set `CANTORA_API_KEY` to a Cantora ServicePrincipal API key rather than putting the credential in HCL. The provider uses `https://api.cantora.ai` by default; `CANTORA_ENDPOINT` overrides it for testing. See the generated [provider](docs/index.md) and [Agent Configuration](docs/resources/agent_configuration.md) documentation for the complete schema.

## State and lifecycle boundary

Agent instructions are reviewable customer data, so they enter saved plans and state. Protect both artifacts with encryption, access control, locking, backup, and retention. Never put credentials, tokens, Connection secret material, or provider refresh material in Agent Configuration.

Removing the resource relinquishes Terraform's local binding only. It does not delete or retire the Agent Definition, roll back behavior, or remove Cantora's immutable history. Import uses `organization_id/project_id/agent_definition_id`. A create never silently adopts a Project key that another state or client already manages.

## Develop

Go 1.26.5 and Terraform 1.13 or later are required.

```sh
go test ./...
go vet ./...
TF_ACC=1 go test -v ./internal/provider
make generate
```

Acceptance tests run against an in-process fake Management API and do not require a Cantora credential. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full validation contract and [SECURITY.md](SECURITY.md) for vulnerability reporting.

Published versions are signed, immutable semantic-version releases. A defect is corrected in a new version; an existing tag or release asset is never replaced.
