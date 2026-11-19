# CLAUDE.md — ovumcy-web

Ovumcy: self-hosted cycle-tracking web app. Go backend, server-rendered templates with a
vanilla-JS frontend bundle.

## Stack

- **Go 1.26** — Fiber v3 (HTTP), GORM (Postgres via pgx, or SQLite via glebarez)
- **Auth** — OIDC (`go-oidc`) + TOTP (`pquerna/otp`), JWT
- **Frontend** — no framework; JS/CSS bundled by `scripts/build-js.mjs` / `build-css.mjs`
- **Tests** — Go stdlib + `pgregory.net/rapid` (property-based); Playwright for e2e

## Commands

```bash
# Full Go suite runs on GitHub Actions (ci.yml) — do NOT run `go test ./...`
# locally on the homelab host (4 cores; the whole-suite binary saturates a
# core + ~3GB, 2026-11-19). Local verification:
gofmt -l .            # formatting
# scoped past node_modules/, where a vendored JS dep ships a .go file
go vet ./cmd/... ./internal/... ./migrations/... ./scripts/... ./web/...
go build ./...
# targeted single-package tests only when iterating:
#   go test ./internal/<pkg>/ -run TestName

npm run build          # build:css + build:js
npm run lint           # eslint, --max-warnings=0
npm run test:unit      # node --test on web/src/js/__tests__
go run ./cmd/ovumcy    # run locally
```

E2E (Playwright, via `scripts/run-e2e.mjs`):
```bash
npm run e2e            # stable mode, chromium
npm run e2e:fast       # fast mode
npm run e2e:postgres   # against postgres instead of sqlite
npm run e2e:cross-browser
```

## Layout

- `cmd/ovumcy/` — entry point
- `internal/` — `api`, `bootstrap`, `cli`, `db`, `httpx`, `i18n`, `models`, `reminders`,
  `security`, `services`, `templates`, `testdb`
- `migrations/` — schema migrations
- `web/` — templates, `src/js`, `static/`
- `e2e/` — Playwright specs

## Deployment

Ships as a signed container: `ghcr.io/ovumcy/ovumcy-web`. Images are Cosign-signed (keyless
via GH Actions OIDC) with SLSA provenance and an SBOM. Local `docker-compose.yml` included.

Runs on the homelab; sends reminders via the ntfy topic `ovumcy`. The reminder webhook URL is
stored encrypted in the DB and is lost if `SECRET_KEY` is rotated.

## Docs

`README.md`, `TESTING.md`, `CONTRIBUTING.md`, `SECURITY.md`, `docs/`.
