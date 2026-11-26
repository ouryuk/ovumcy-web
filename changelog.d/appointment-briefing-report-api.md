### Added

- **Appointment briefing: `GET /api/v1/report?from=&to=` (owner only).**
  One JSON answer an owner can hand a clinician: the logged-day summary the export summary
  publishes, every completed cycle that overlaps the range in exactly the shape
  `GET /api/v1/stats/cycles` publishes, and how many distinct logged days each symptom was
  recorded on, ordered by count descending then name. The range aggregates come from one
  `ExportService` read and the cycles reuse the shared cycle-history builder and detector, so
  the briefing cannot disagree with either existing surface about the same window. An
  unparseable or reversed range is a `400` before any data is read; an empty range is a `200`
  with `has_data: false` and empty lists, never an error. The request is audited as
  health-data egress under the same `data.export` action as the CSV/JSON/summary downloads,
  with `export_format: "briefing"`; no payload ever enters the log line, and the response
  carries the medical disclaimer. Declared in `docs/openapi.yaml`
  (`AppointmentBriefing`, `BriefingSymptomCount`, both `additionalProperties: false` and
  pinned to the DTOs by the spec-pin test). The printable `/report` HTML page is deliberately
  future work: the API exists so a page and external clients cannot drift on the briefing's
  definition.
