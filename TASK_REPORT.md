# Task report: appointment briefing prototype (`GET /api/v1/report`)

## Why

Two of the three candidate gaps from the task brief have already shipped: the
read-only cycle-history API (`GET /api/v1/stats/cycles`, declared in
`docs/openapi.yaml` line ~2100 and merged from
`docs/proposals/cycle-history-api.md`) and the token-authenticated `.ics`
calendar feed (`GET /calendar/feed/:token.ics`, covered by the whole
*Calendar feed subscription* section of `docs/SECURITY_INVARIANTS.md` and its
restore-fence machinery). Building near either would have been re-work.

The remaining highest-value gap was the **appointment briefing**: an owner
walking into a clinician's office today has to open `/exports/summary`,
`/exports/json` and `/stats` side by side and assemble the story by hand. No
single read answers "what happened between these dates" — logged days, which
completed cycles ran through the window, and which symptoms were recorded how
often. The data and the services that read it already exist; only the composed,
owner-scoped, audited read was missing.

## What

- **`GET /api/v1/report?from=&to=`** — owner-scoped (`AuthRequired` +
  `OwnerOnly`), JSON only, additive. `from`/`to` are the same inclusive
  `YYYY-MM-DD` bounds the export endpoints accept; an unparseable or reversed
  range is `400` before any data is read, an empty range is `200` with
  `has_data: false` and empty lists (never `404`/`500`).
- The response carries:
  - `total_entries`, `has_data`, `date_from`, `date_to` — the export summary's
    aggregate, where the dates are the span of the logged entries themselves;
  - `completed_cycles` — the `/api/v1/stats/cycles` entries filtered to every
    cycle **overlapping** the range (a clinician asking about "May" gets the
    cycle that runs through May even though it started in April);
  - `symptom_frequency` — per-symptom count of distinct logged days, ordered by
    count descending then name; built-in symptoms use their catalogued column
    key, custom symptoms their trimmed name;
  - `disclaimer` — the medical-safety disclaimer in the request's language.
- Both list fields are always non-nil `[]` on the wire.
- **Audited as health-data egress** under the existing `data.export` action
  with `export_format: "briefing"`, so one operator filter covers every format;
  refused requests are logged like the exports', and no payload ever enters a
  log line.
- **OpenAPI**: `/api/v1/report` declared with the standard `200/400/401/403/429`
  set; new `AppointmentBriefing` and `BriefingSymptomCount` schemas with
  `additionalProperties: false` and full `required`, pinned to the DTOs by a
  spec-pin test in both directions.

## How

Additive only; no schema change, no migration, no new dependency, no new
service wiring.

- `internal/services/appointment_briefing.go`
  - `(*ExportService).BuildBriefingSummary` — the range summary plus the
    symptom frequency from **one** `LoadDataForRange` read (the same read path
    the JSON/CSV exports use), counting each symptom once per logged day.
  - `FilterBriefingCycles` — a pure calendar-day overlap filter over the
    existing `CycleHistoryEntry` values; the cycles themselves come from the
    unmodified `StatsService.BuildCycleHistory`, so the briefing cannot
    disagree with `/api/v1/stats/cycles` about which cycles exist.
- `internal/api/handlers_report.go` + `internal/api/report_briefing_dto.go` —
  the handler reuses `exportUserAndRange` (auth + range parsing + egress
  attribution), `newCycleHistoryResponse` (the cycles keep the exact published
  shape), the existing error mappings, and `medicalDisclaimer`.
- `internal/api/routes.go` — `report := v1.Group("/report", handler.AuthRequired)`
  with `report.Get("", handler.OwnerOnly, handler.GetReport)`.

### Tests (written; run on GitHub Actions per the no-local-go rule)

- `internal/services/appointment_briefing_test.go` — day-deduped counting,
  count-then-name ordering, error pass-through, overlap-filter semantics
  (straddle both bounds, both-bounds-nil, nil-result-non-nil).
- `internal/api/report_briefing_contract_test.go` — real request path:
  composed payload (cycles + summary + frequency + disclaimer), range
  filtering, `200`/empty for an owner with no logs, owner isolation (second
  owner sees nothing), `400` on reversed/unparseable ranges.
- `internal/api/report_briefing_spec_pin_test.go` — both new schemas pinned
  against their DTOs, required included.
- `changelog.d/appointment-briefing-report-api.md` fragment added.

### Security-invariant compliance

Owner-only chain on the route; every read is scoped by `user.ID` via the
existing services (the egress-ownership stub pattern in the services tests
covers the acting-owner operand); the audit line follows the
*Privacy and PII* egress rules verbatim (fixed action/target, format detail
only); no secret, token, or new cookie is introduced; no `template.HTML` —
JSON transport only. The briefing is strictly read-only, so it needs no
re-authentication, exactly like the exports.

## Next steps

1. **`/report` HTML page.** Render the same briefing as a printable page for
   owners who want paper for the appointment; the API now defines the shape so
   page and clients cannot drift. Out of prototype scope (templates + i18n).
2. **CSV variant** of the briefing (one summary row + one cycle row + one
   frequency row) for clinician hand-off.
3. **Default window**: consider `?days=90` sugar; the current bounds-only
   contract matches the export endpoints deliberately.
