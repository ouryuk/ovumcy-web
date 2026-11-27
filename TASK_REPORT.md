# Proposal: `ovumcy notify --preflight` — why an owner's reminders are (not) arriving

## Why

The shipped pass (`internal/services/webhook_notify_service.go` `RunOnce`) reports only
aggregates: owners scanned, due, sent, failed, and `OwnerIDsFailed`. Its per-owner detail is
host-only `log.Printf` lines (`decrypt failed, skipping owner id=N`, `load logs failed`, and the
deliverer's `reason=… host=…`). Everything that keeps an owner silent *without* failing — delivery
disabled, no endpoint, no reminder kind, predictions suppressed, already sent, not due — writes
no line at all, and a disabled or unreadable owner never reaches the deliverer, so a real pass
counts them as nothing.

The consequence is the operator's actual question: "reminders stopped for this owner — why?" The
answer exists inside the pass but only as aggregate counters plus a log to grep for an id. A
diagnostic that *only* walked the happy path could not answer it either: the interesting owners
are precisely the ones a `--dry-run` preview omits (the preview is a catalogue of what would be
sent). So the seam had to live inside the pass's own branches.

## What

**1. A per-owner outcome on the pass (`internal/services/webhook_notify_preflight.go`).**

Each branch `processOwner` already takes now also writes a `PreflightOutcome` — owner id,
destination **host only**, due count, a stable reason key, an optional detail, and whether the
operator can fix it — to a **per-pass recorder** (`*preflightRecorder`) passed into the walk.
The recorder is nil for a normal `RunOnce`, so nothing about delivery, claims, marks, watermarks,
counters, or the existing log lines changes; the recorder is per-call, so the service stays
stateless and two concurrent walks cannot see each other's outcomes.

Reason enum (published contract, `omumcy-notify-preflight/v1`):

| key | meaning | actionable |
| --- | --- | --- |
| `ok` | a reminder is due and would be delivered (`due` = how many) | no |
| `delivery_disabled` | the master switch is off; re-enable with `ovumcy webhook set <email> --enabled=true` | **yes** |
| `no_endpoint` | enabled, but no endpoint stored; save one with `ovumcy webhook set <email> --url-stdin` | **yes** |
| `endpoint_unreadable` | ciphertext cannot be opened (SECRET_KEY rotation) | **yes** |
| `endpoint_invalid` | unparseable / non-http(s) / hostless authority | **yes** |
| `private_address_blocked` | private literal + `WEBHOOK_BLOCK_PRIVATE_ADDRESSES`; repoint the target or turn the gate off | **yes** |
| `logs_unreadable` | the decision could not run | **yes** |
| `predictions_suppressed` | unpredictability mode / pregnancy pause / overdue cycle | no (by design) |
| `no_reminder_kind` | both reminder switches off; enable one with `ovumcy webhook set <email> --notify-period=true` | **yes** |
| `already_sent` | this cycle's watermark covers it | no |
| `not_due` | nothing inside the lead window | no |
| `delivery_failed` | only a real pass; `detail` = `transport_error` / `redirect_refused` / `non_2xx` / `scheme_not_allowed` / `private_address_blocked` / `claim_error` | **yes** |
| `claim_lost` | only a real pass; concurrent pass or revoke mid-pass | no |

**Precedence** when several blocks apply — first match wins: master switch → missing endpoint →
unreadable endpoint → endpoint shape/egress gate → unreadable logs → cycle state
(`no_reminder_kind`, `predictions_suppressed`, `already_sent`, `not_due`) → `ok` → (real pass only)
`delivery_failed` / `claim_lost`. Rationale: a structural block (can't decrypt, can't reach) is the
defect worth fixing regardless of cycle state, so it outranks both "nothing due" and "would
send". `actionable` means the reason names something a change in Ovumcy can fix — a configuration
choice, a correctable defect, or a failed delivery — as opposed to a by-design gate
(`predictions_suppressed`), a normal cycle state (`already_sent`, `not_due`), success (`ok`) or a
concurrent pass winning the claim (`claim_lost`); the CLI's "Act now" prefix and its
"N owner(s) need attention" count are exactly that flag. `due` is always the decision's own count,
so a blocked owner still shows how many reminders are waiting.

**Design call — reuse the dry-run path or walk separately?** A **dedicated read-only walk that
reuses the same `run`**: `NotifyPreflight` calls `service.run(..., dryRun=true, recorder)` — the
exact traversal, decision and preview a `--dry-run` runs — rather than a parallel implementation
or a second pass over the report. The only extra work is resolving the suppression reason for
owners with nothing due (`BuildCycleStatsFromLogs`, pure and repository-free, and only on that
branch). A delivery pass pays nothing because no recorder is attached. This is why the diagnostic
cannot drift from what a pass decides.

**2. `ovumcy notify --preflight [--owner <id>] [--json]` (`internal/cli/notify_preflight.go`).**
Table: `OWNER | HOST | DUE | REASON | NEXT ACTION`, then "N owner(s) need attention". JSON:
`ovumcy-notify-preflight/v1` with `schema`, `generated_at`, `owners_scanned`, `owner`/`matched`
(only when filtered), and `owners[]` of `owner_id`, `host`, `due`, `reason`, `detail` (omitted when
empty), `actionable`, `next_action`. `--owner` narrows the *printed* rows only; the walk always
covers everyone; an unmatched filter is an **error**, never an empty healthy table. `--preflight`
with `--fail-on-delivery-error`, or `--json`/`--owner` without `--preflight`, is a usage error.

**Secret discipline:** neither rendering carries a URL, path, query, userinfo, token, ciphertext,
response body, reminder type, or estimated date — so unlike `--dry-run --show-health-details`, both
are safe in an operator log. Host comes from the existing `hostOnly`, the package's single
implementation of that rule.

**3. Operator surfaces:** `docs/notifications.md` gains a `--preflight` subsection (usage, sample
output, the reason table, the precedence rule, the no-DNS caveat, the flag pairings);
`docs/SECURITY_INVARIANTS.md` names the preflight's output line beside the existing notify bullet;
`docs/self-hosted.md`'s SECRET_KEY-rotation guidance now points at the preflight as the place the
pass names the cause; `changelog.d/feat-cli-notify-preflight.md`.

## How (tests)

- `internal/services/webhook_notify_preflight_test.go` — a table with one case per reason key
  (reason, host, due, actionable together), the endpoint-shape table and delivery-key table, the
  delivery-failure-overwrites-ok case with the recorder attached (counters unchanged), per-owner
  row ordering, a concurrent-walk statelessness check, and a leak guard proving userinfo/query/
  port/path never reach the outcome.
- `internal/cli/notify_test.go` — flag contract (`--owner <id>` / `--owner=<id>`, refused pairings),
  table rendering (no scheme, token, date or reminder type), JSON wire shape decoded into a map so
  the assertion is about the document not the struct, the owner filter, an advice-table guard that
  keeps "Act now"/"Nothing to do" in step with `Actionable`, and a zero-owner end-to-end run through
  the real wiring.

## Next steps

- **A preflight of the scheduled pass from the server process**: the built-in scheduler
  (`internal/reminders`) drives `RunOnce`; attaching a recorder there and logging one line per
  actionable owner would put the same verdict in the journal without a shell.
- **Resolve-and-check at preflight time, optionally**: `private_address_blocked` today reports only
  IP-literal targets (no DNS in a diagnostic). A `--resolve` opt-in could reuse the guarded dialer's
  classifier so a private *hostname* is reported before the pass runs.
- **`--owner <email>`**: needs the operator user lookup; deliberately absent so the diagnostic reads
  nothing beyond the notify projection.
- **Out of scope, unchanged**: the hard-kill-between-claim-and-release caveat in
  `docs/notifications.md` still needs the schema decision it describes; the preflight deliberately
  makes no claim, so it neither depends on nor works around it.