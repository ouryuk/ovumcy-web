### Added

- **A CSV export can now be restored, not only the JSON one.** `POST /api/v1/imports/csv`
  accepts the file `GET /api/v1/exports/csv` downloads and recreates its days — only the ones
  the account does not already have. Existing days are left untouched, every cell is
  re-validated and normalized server-side (labels back to their enums, `;`-separated cycle
  factors and custom symptom names back into the owner's catalog), and a malformed header,
  bad quoting, invalid date or duplicate row is reported in the same `added` / `skipped` /
  `rejected` counts the JSON restore returns. Like the JSON restore it is owner-only and
  CSRF-protected, and needs no re-authentication because it deletes nothing. See
  [docs/export.md](docs/export.md#csv-import).
