# Contributing to Aether

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

By contributing, you agree that your contribution is licensed under AGPL-3.0-or-later.
