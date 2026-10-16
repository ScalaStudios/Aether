## What changed

## Why

Closes #

## How you verified it

- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass
- [ ] The integration tests pass: `docker compose --profile test run --rm --no-deps integration-tests`
- [ ] `npm run lint` and `npm run build` pass in `apps/web`
- [ ] Tests cover any change in behaviour, and new endpoints are covered in `tests/integration/tenant_test.go`
- [ ] `api/openapi.yaml` is updated with any change to the API, and schema changes are a new file in `migrations/`
- [ ] The README or `documentation/` is updated, if people or operators see the change
- [ ] Screenshots are attached, for interface changes

## AI assistance

- [ ] No AI tool wrote code, tests or text in this pull request
- [ ] An AI tool helped. I have read every line, I understand it, and I ran the checks above myself

If an AI tool helped, name it and say which parts it wrote:

## Fork

- [ ] This branch is in a fork, and **Allow edits by maintainers** is turned on

By opening this pull request, you agree that your contribution is licensed under AGPL-3.0-or-later.
