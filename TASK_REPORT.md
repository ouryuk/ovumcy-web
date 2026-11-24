# Task report: `ovumcy predict` — a read-only next-cycle-window CLI

## Why

The dashboard, the calendar feed and the webhook notify pass already project each owner's next
period start, ovulation date and fertile window — but only behind a login. The repo's recent
scout additions (summary, trends, export) built a shell-side, read-only family of operator
commands, and the one analytics gap left open was exactly the suggestion in the scout brief:
a **predictive next-cycle window** was still missing. An operator (or a monitoring script) had
no way to answer "what is this account's projected window as of today?" without opening the
browser app or reverse-engineering the derivation. There is no roadmap/TODO naming an unstarted
item, so this gap is taken as the prototype, in the same style as `feat-cli-trends-phase-analytics`.

## What

- New read-only CLI subcommand: `ovumcy predict <email> [--id <id>] [--tz <IANA name>] [--json]`.
- Human table, or a versioned `ovumcy-predict/v1` JSON document, reporting:
  - where the owner stands today: cycle day, phase, fertility status, pregnancy pause;
  - the **basis** the projection was computed from — completed cycles, average/median/predicted
    cycle length, predicted period length, personalised luteal phase — so an operator can judge
    how much to trust the numbers (twenty observed cycles are not an onboarding default);
  - the projection: last period start, next period start, ovulation date (+ `ovulation_exact`
    clamp flag), six-day fertile window;
  - the projection disclaimer in every rendering — script and human can never disagree about it.
- Explicit degradation: a pregnancy pause prints "predictions are suppressed"; an owner with no
  cycle history prints "nothing can be projected yet" instead of an empty document.

## How

- `internal/cli/predict.go` — argument parsing mirrors `trends.go` exactly (same email/id
  vocabulary via `resolveOperatorUser`, same `--flag value` / `--flag=value` forms, single usage
  string). All numbers come from the existing `services.BuildCycleStatsFromLogs` — the exact
  derivation the dashboard uses — so the CLI cannot drift from what the owner sees. The full
  `models.User` row is re-fetched because the luteal/baseline pass consumes the onboarding
  fields the operator summary does not carry.
- Date rendering uses `CalendarDayKey` semantics (value's own calendar components): the baseline
  anchors carry request-location wall clocks, and a `UTC()` conversion would shift the printed
  day by one for non-UTC `--tz` — caught by the `Europe/Madrid` test.
- `cmd/ovumcy/commands.go` — `predict` dispatch wired like `summary`/`trends` (database config
  only; no secret is read; nothing is written).
- Tests `internal/cli/predict_test.go`: seeded 28-day fixture pinned to 2026-03-27 (day 3 of a
  live cycle) asserts the full JSON projection (next period 2026-04-22, ovulation 2026-04-07,
  fertile window 2026-04-02→04-07, basis 3 cycles / median 28), the human table, `--id` +
  `--tz` handling and the bad-arg contract, the no-history path, and pregnancy-pause
  suppression. Clock pinning goes through the `predictNow` variable, mirroring `summaryNow`.
- Changelog fragment `changelog.d/feat-cli-predict-next-cycle-window.md`.
- Verified locally per CLAUDE.md: `gofmt -l` clean, `go vet ./cmd/... ./internal/... ./web/...`
  clean, `go build ./...` clean; scoped tests run for the two touched packages
  (`internal/cli` predict tests + full `cmd/ovumcy` suite) pass. Full Go suite is CI-gated
  (ccb751bb). Internal `internal/cli/zz_idzero_test.go` left untouched.

## Next steps

- If the prototype is adopted, consider surfacing `IsIrregularCycleSpread` (already computed in
  services) as a `cycle_basis.irregular` field so scripts can gate trust programmatically.
- A `--days N` horizon (project N cycles forward via `appendPredictedCycles`-style stepping)
  would extend this without any new derivation.
- The summary/trends/predict commands share an argument-parsing shape now in three copies; a
  shared `operatorTargetArgs` helper would be a small, safe follow-up refactor (deliberately not
  done here — additive only).
