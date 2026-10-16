# Contributing to Aether

Bug reports, fixes, documentation and features are welcome. Open an issue before starting anything large, such as a new feature, a new dependency or a schema change, so the approach is agreed before the code is written. Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Forks and pull requests

1. Fork the repository and create a branch from `main`.
2. Make the change, with tests, and run the checks below.
3. Open a pull request against `main` and fill in the template. Turn on **Allow edits by maintainers** so small fixes can be pushed to your branch.

Aether is licensed under AGPL-3.0-or-later, so you are free to run a fork. If you let people use a modified Aether over a network, the license requires you to offer them its source code.

## Development setup

```sh
./scripts/setup.sh dev
docker compose up -d --build
cd apps/web && npm ci && AETHER_API_URL=http://localhost:8080 npm run dev
```

The web dev server proxies `/api` to the URL in `AETHER_API_URL`.

## Before opening a pull request

Run these checks:

- `gofmt -l .` prints nothing
- `go vet ./...` and `go test ./...` pass
- `docker compose --profile test run --rm --no-deps integration-tests` passes; it needs `AETHER_TEST_ADMIN_EMAIL` and `AETHER_TEST_ADMIN_PASSWORD`
- `npm run lint` and `npm run build` pass in `apps/web`

## Guidelines

- Change the API contract in `api/openapi.yaml` in the same pull request as the handler.
- Add schema changes as a new numbered file in `migrations/`. Never edit a migration that has shipped.
- Every new endpoint must check tenant scope, and `tests/integration/tenant_test.go` must cover it.
- Keep code free of comments. Choose names that explain the code.
- UI work follows the Scala Studios brand guidelines:
  - the 4 pt spacing scale
  - Archivo, Inter, and JetBrains Mono
  - Ember as the single accent colour
  - Ink labels on Ember buttons

## AI-assisted contributions

You may use AI tools, but you are responsible for every line you submit. Say in the pull request which tool you used and which parts it wrote. Read and understand the code before you open the pull request, and run the checks yourself rather than relying on a tool's report that they pass. Pull requests that look generated and unchecked, such as changes that do not build, invented APIs or descriptions that do not match the diff, are closed.

By contributing, you agree that your contribution is licensed under AGPL-3.0-or-later.
