# Contributing

Thank you for improving the Cantora Terraform provider.

Open an issue before starting a large behavior or schema change so the Management API and provider contracts can evolve together. Keep the provider as a narrow Management API client: server-owned validation, authorization, release policy, and immutable history do not belong in provider-side reimplementations.

## Development

Install Go 1.26.5 and Terraform 1.13 or later, then run:

```sh
go test ./...
go vet ./...
TF_ACC=1 go test -v ./internal/provider
make generate
git diff --exit-code
```

`make generate` formats examples and regenerates Registry documentation. Generated files under `docs/` must be committed with schema or example changes.

Add unit coverage for API encoding, authentication, error mapping, cancellation, response bounds, and secret redaction. Resource behavior changes need Terraform CLI acceptance coverage. A later state-schema change also requires an upgrade fixture from every supported prior schema version.

Never include a Cantora credential, customer Agent Configuration, Terraform state, saved plan, or other secret-bearing artifact in a test fixture, issue, log, or pull request.
