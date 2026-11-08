# Proposal: read-only cycle history API

## Why

Ovumcy already answers aggregate questions — `/api/v1/stats/overview` reports
averages, medians, a completed-cycle count and the current prediction — and it
exposes a cycle-length trend series. What neither an owner nor an external
client can ask today is the concrete, per-cycle question:

> What were my last cycles, one by one — when did each start and end, how long
> was it, and when was ovulation?

That gap matters for journals, clinician conversations, third-party dashboards
and any future export or comparison view. The data already exists in
`daily_logs` and the cycle detector already derives it for the overview and the
stats page; there was simply no owner-scoped read-only endpoint that returned
the individual cycles.

This prototype adds that endpoint without changing the schema, the existing
detection algorithm, or any existing exported behaviour.

## What

### Endpoint

`GET /api/v1/stats/cycles`

- Tagged `stats`, alongside `GET /api/v1/stats/overview`.
- Owner-scoped through the existing `AuthRequired` group and `OwnerOnly`
  middleware, like every other `/api/v1` route.
- Returns a JSON array, oldest cycle first. An owner with no completed cycles
  receives `200 []`, never `404` or `500`.
- The running cycle is not included: it has no known end or length yet.

Each entry has this shape:

```json
{
  "start_date": "2026-01-01",
  "end_date": "2026-01-28",
  "length": 28,
  "ovulation_date": "2026-01-14",
  "ovulation_confirmed": false,
  "ovulation_estimated": true
}
```

- `start_date` / `end_date` are calendar days (`YYYY-MM-DD`), not RFC 3339
  instants.
- `length` is the calendar-day span to the next observed cycle start, the same
  interval the overview's completed-cycle statistics use.
- `ovulation_date` is either inferred from the owner's own BBT shift
  (`ovulation_confirmed: true`) or estimated from the cycle length and the
  owner's luteal phase (`ovulation_estimated: true`). The two flags are
  mutually exclusive. The date is `null` when the cycle is too short for either
  source to place one; both flags are then `false`.
- `GET /api/v1/stats/overview` is unchanged, as is every existing route, status
  code and declared break.

### OpenAPI

`docs/openapi.yaml` declares:

- the new `GET /api/v1/stats/cycles` operation with `200`, `401`, `403` and
  `429` responses, matching the other owner-only stats operation;
- a `CycleHistoryEntry` schema with `additionalProperties: false` and every
  wire field in `required`.

## How

### Service layer

`internal/services/cycle_history.go` adds:

- `CycleHistoryEntry` — the domain result for one completed cycle.
- `(*StatsService).BuildCycleHistory(ctx, user, now, location)`.

The builder deliberately reuses the existing pipeline rather than adding a
second cycle-detection implementation:

1. `StatsService.BuildOverviewStats` performs the same two-year log read the
   aggregate overview performs and applies the owner's baseline. The history and
   the overview therefore see the same logs and cannot disagree about which
   cycles exist.
2. `ObservedCycleStarts` and `buildCycles` produce the existing
   `detectedCycle` boundaries. A cycle is published only when a later observed
   start closes it.
3. `inferBBTOvulationDate` — the shared "3-over-6" detector used by the
   calendar, dashboard and stats chart — supplies a confirmed ovulation day
   inside each completed cycle. When no shift is found, `PredictCycleWindow`
   estimates the day from that cycle's length and the owner's luteal phase.

No database schema change, migration, or new cycle algorithm is involved. The
only fallible step is the existing repository read; its error is passed through
to the existing `statsFetchErrorSpec()` mapping.

### Transport layer

- `internal/api/handlers_stats.go` adds `GetCycleHistory` next to
  `GetStatsOverview`.
- `internal/api/stats_cycle_history_dto.go` maps service entries to the wire
  DTO and always returns a non-nil slice.
- `internal/api/routes.go` registers the route beside `/overview`.

### Tests

Service tests (`internal/services/cycle_history_test.go`) cover:

- one entry per completed cycle, in chronological order, with start/end/length;
- entry length and end matching `buildCycles` for the same logs;
- empty history returning `[]` rather than `nil` or an error;
- owner isolation through the repositories;
- estimated ovulation from cycle length;
- confirmed ovulation from a BBT shift;
- repository errors passing through.

API tests (`internal/api/stats_cycle_history_contract_test.go`) exercise the
real request path and cover:

- the chronological completed-cycle list and its dates/lengths/flags;
- `200 []` for an owner with no logs;
- a second owner seeing none of the first owner's cycles;
- a spec pin (`stats_cycle_history_spec_pin_test.go`) asserting that the
  `CycleHistoryEntry` schema enumerates exactly the DTO's wire fields, both
  ways, with `additionalProperties: false`.

The existing route/spec/status contract tests pass unchanged, proving the new
operation is declared and reachable and that no existing route, status code, or
declared break moved.

### Verification

Run from the repository root:

```text
go build ./...                                      exit 0
go vet ./internal/api/... ./internal/services/...   exit 0
go test ./internal/services/...                     exit 0
go test ./internal/api/...                          exit 0
```

The full `go test ./...` suite was not run; the two affected package trees above
were run and pass. `go test ./internal/api/...` is the slower half (~13 min on
this host).

## Next steps

1. **Render the list on `/stats`.** Add a "cycle history" section to
   `internal/templates/stats.html`, fed by the same
   `BuildCycleHistory` result. The API exists so the page and external clients
   cannot drift on the per-cycle definition. This is explicitly out of scope for
   the prototype.
2. **Per-cycle CSV export.** Add a CSV variant of the history (one row per
   cycle) for journals and clinician hand-off, reusing the DTO fields rather
   than re-deriving them.
3. **Cycle-comparison view.** Compare cycles side by side — length, ovulation
   day, period length, logged symptoms — using the same entries as the single
   source of truth.
