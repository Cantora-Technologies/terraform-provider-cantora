## What changed

Describe the provider, API contract, documentation, or release behavior changed by this pull request.

## Verification

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `TF_ACC=1 go test -v ./internal/provider`
- [ ] `make generate` leaves no diff
- [ ] No credential, customer configuration, state, or saved plan is included

## State and compatibility

Describe any schema, import, upgrade, destroy, or release compatibility effect. Write `None` when there is none.
