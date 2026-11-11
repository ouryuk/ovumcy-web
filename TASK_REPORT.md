# Task report: owner-scoped CSV import

## Why

Ovumcy could export an owner's health data as CSV (`GET /api/v1/exports/csv`) but could not
restore one. The only restore path accepted the JSON export, so an owner who had kept the
human-readable CSV — the format most spreadsheet users actually archive — had no way back in.
The JSON restore already defines the safe contract (additive, owner-scoped, fully re-validated,
CSRF-protected, no re-authentication because nothing is destroyed); this change gives CSV the
same contract instead of a second, weaker import path.

## What

- `POST /api/v1/imports/csv` (`owner` + `AuthRequired`, CSRF-protected by the global middleware)
  accepts a file shaped exactly like the documented CSV export.
- The header must name every documented export column exactly once; order, case and surrounding
  whitespace are insensitive. A missing/unknown/duplicated column, bad quoting, or a row with the
  wrong cell count is refused as `400 invalid import file` before any write.
- Rows are normalized server-side through the existing day policies: labels map back to their
  enums, `Yes`/`No` becomes a boolean, `BBT (C)` is parsed as Celsius (empty/unparseable means
  "not measured"), `;`-separated cycle factors and custom symptom names are resolved, notes are
  length-capped, and the exporter's CSV-injection apostrophe is removed only when it protects a
  formula-like prefix.
- Invalid and duplicate `Date` rows are counted as `rejected`; the rest of the file still imports.
- The write path is shared with the JSON restore (`ImportService.restore`), so the additive
  skip-existing contract, batched inserts, transaction handling, symptom reconciliation and
  derived-settings refresh cannot drift between formats.
- Response is the existing `ImportResult` shape (`ok`/`added`/`skipped`/`rejected`), audited on
  the same `data.import` health mutation line with the same counts.
- Docs: `docs/export.md` (CSV import section plus the shared error-key note),
  `docs/openapi.yaml` (new operation), `docs/SECURITY_INVARIANTS.md` (restore invariant now names
  both formats), and `changelog.d/csv-import-restore.md`.
- Tests: service-level round trip, additive skip, owner isolation, reordered/padded headers,
  malformed shapes, row cap, invalid/duplicate dates, label/garbage normalization, plus API-level
  CSRF refusal, happy path, additive + owner scoping, malformed file, and audit tagging.

## How

1. Read the last 30 days of history first. No files were deleted or renamed in that window, and
   nothing was reverted or rewritten.
2. Traced the existing JSON import (`internal/services/import_service.go`,
   `internal/api/handlers_import_json.go`) and extracted its post-parse tail into
   `ImportService.restore` so CSV reuses the exact write path.
3. Added `internal/services/import_csv.go`: BOM-tolerant `encoding/csv` parsing, strict
   header-to-canonical-column resolution derived from `ExportCSVHeaders()`, per-cell label
   reversal, and the same `MaxImportEntries` / `ErrImportMalformed` / `ErrImportTooLarge` errors.
4. Added the transport handler `internal/api/handlers_import_csv.go` and registered
   `POST /api/v1/imports/csv` beside `/json`.
5. Added focused tests in `internal/services/import_csv_test.go` and
   `internal/api/imports_csv_regressions_test.go`.
6. Verified: `go test ./cmd/... ./internal/services/... ./migrations/... ./scripts/... ./web/...`,
   full `go test ./internal/api/`, `go run ./scripts/archcheck`, `npm run lint`,
   `npm run test:unit`, `npm run build`, and a fresh coverage profile through
   `scripts/patchcov` against `origin/main` (gate OK).

## Next steps

- Wire the CSV restore into the Settings → Import UI (the endpoint is API-complete; the settings
  form still posts only JSON). This would also need the locale strings mirrored across the six
  supported locales.
- Consider a pre-flight "dry run" mode if owners ask to see the `added`/`skipped`/`rejected`
  split before committing; the service currently reports it only after the additive write.
- Revisit the minimal header policy if third-party CSV import (e.g. Drip, issue #116) is ever in
  scope: that would need a mapping layer rather than requiring Ovumcy's own export header.
- The CSV-injection apostrophe reversal is asymmetric for a genuine note that begins with `'`
  followed by a formula prefix; the export itself cannot distinguish the two. If that edge case
  matters, store the raw note and apply the spreadsheet guard only at render/download time.
