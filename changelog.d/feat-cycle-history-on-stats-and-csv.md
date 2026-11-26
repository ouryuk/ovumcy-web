### Added

- **The cycle history is on the stats page, and downloadable as CSV.** `GET /api/v1/stats/cycles`
  shipped the per-cycle list to API clients only; the stats page still showed just the aggregates.
  The page now has a server-rendered **Cycle history** section — one row per completed cycle with
  start, end, length, the ovulation day and whether that day was confirmed by temperature,
  estimated or not placed — with an empty state while there are no completed cycles. The same
  entries are available to journals and clinician hand-off as
  `GET /api/v1/stats/cycles?format=csv` (one row per cycle, the table's columns, fixed English
  header names), owner-scoped by the existing middleware and audited as `data.export` egress with
  `export_format="stats_cycles"`. Page, JSON and CSV all render the one `BuildCycleHistory`
  result, so they cannot drift on what a completed cycle is — which is what the cycle-comparison
  view builds on next.
