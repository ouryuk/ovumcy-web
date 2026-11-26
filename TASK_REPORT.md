# Proposal: `ovumcy forecast-accuracy` — scoring the cycle prediction against its own history

## Why

`ovumcy predict` (and the dashboard/feed behind it) answers "when is the next cycle", but nothing in
the repo answers the follow-up an operator or an owner reasonably asks next: **how well is that
prediction actually holding up?** The read-only CLI family (`export`, `summary`, `trends`,
`predict`) all report current state; none of them reports the model's own historical accuracy. The
math is documented and deterministic (`docs/cycle-prediction.md`), which makes it *scoreable* — every
completed cycle is a prediction target whose training data (the cycles before it) is fully known. A
reproducible accuracy scorecard closes the loop: it tells an operator whether the projection on the
dashboard deserves trust for *this* account, without a login and without recomputing anything by
hand. This is additive only: no schema change, no deployment change, no write path.

## What

- New read-only, no-secret CLI subcommand next to the family:
  `ovumcy forecast-accuracy <email> [--id <id>] [--tz <IANA name>] [--json]`.
- For each completed cycle, it recomputes the projection the model **would have made** from only the
  cycles before it — `services.BuildCycleStatsFromLogs` run at an explicit past "as of" date (the
  actual cycle start), with logs strictly before that day — and reports:
  - per cycle: predicted vs actual start, **signed error in days** (positive = model was early),
    whether the actual start fell inside the model's own recent length range
    (`LastPeriodStart + Min/MaxCycleLength`), and the basis it predicted from;
  - summary: mean and median absolute error, worst cycle, and how many predictions landed inside the
    model's own window (count + percentage);
  - human table by default, versioned `ovumcy-forecast-accuracy/v1` JSON behind the existing
    `--json` convention, disclaimer in both.
- Honest degradation: a cycle the model would not have predicted at all — pregnancy pause, or too
  little preceding history — is reported **skipped with a reason**, never scored as a zero error;
  zero or one completed cycle prints "accuracy cannot be measured yet".

## How

- `internal/cli/forecast_accuracy.go` — parsing/rendering mirror `predict.go` exactly (same
  email/id resolution via `resolveOperatorUser`, `--flag`/`--flag=value` forms, `--tz` default UTC).
- **No service change was needed.** `BuildCycleStatsFromLogs` already takes an explicit `now` and is
  package-level and clock-free, so the "as of" prediction is just the production derivation called
  with a past date — the CLI cannot drift from what the dashboard would have shown. A tiny
  `filterLogsBeforeDay` enforces the strict training bound (the derivation itself bounds at
  "not after", which would leak the target cycle's own first day).
- The scored cycle starts come from the shared detection, not a second implementation: a two-method
  adapter (`forecastAccuracyLogReader`) plugs the CLI's daily-log repository into
  `services.StatsDayReader` so `StatsService.BuildCycleHistory` enumerates the same completed cycles
  the cycle-history API reports. Only the *starts* are consumed from it; each projection is
  recomputed from the pre-cycle training logs.
- Everything in the scoring path is deterministic and clock-free (the only clock reads are the
  pinnable `forecastAccuracyNow` history anchor and the `generated_at` stamp), so the numbers are
  stable for a given history.
- `cmd/ovumcy/commands.go` — wired as `forecast-accuracy` next to the other read-only dispatches.
- Tests (`internal/cli/forecast_accuracy_test.go`, style of `predict_test.go`): a five-cycle fixture
  with known lengths (28/28/27/28) whose per-cycle errors are provably 0, 0, −1, 0 (asserted row by
  row, with basis, window and full summary: mean 0.3, median 0, worst −1-day cycle, 3/4 in window);
  zero history; one completed cycle (insufficient data); pregnancy-pause skip path; JSON shape and
  `--id`/usage-error handling.
- Docs: README operator-CLI section updated (list, usage lines, one bullet); changelog fragment
  `changelog.d/feat-cli-forecast-accuracy.md`.

## Verification

Not run locally per the task rules (no go/vet/build/test on this host); `gofmt`-canonical alignment
was written by hand. GitHub Actions `ci.yml` is the verdict for build, vet, lint and the Go suite.

## Next steps (beyond this prototype)

- Surface the same scorecard behind the API/UI (an owner-facing "prediction accuracy" insight) — the
  scoring function is deliberately service-shaped and reusable.
- Optionally add an explicit window parameter to the service layer (a documented ±N-day period
  window) if product wants a fixed tolerance band instead of the model's own length range.
- Track accuracy over time (per-quarter MAE) if operators want trend-of-trust rather than
  point-in-time.
