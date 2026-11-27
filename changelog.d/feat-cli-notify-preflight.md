### Added

- **`ovumcy notify --preflight` says why an owner's reminders are not arriving.** The
  request-free pass reported only aggregates — owners scanned, due, sent, failed, plus the
  ids that failed — and its per-owner detail went to the service log as one line per
  reason, so an operator whose webhook reminders silently stopped could see *that* an
  owner was unreachable but not *why* without grepping the pass's log for that owner id.
  The new read-only subcommand (`ovumcy notify --preflight [--owner <id>] [--json]`) runs
  the identical walk in dry-run mode — no request, no claim, no watermark, no write — and
  prints one row per owner: the owner id, the destination **host only**, how many
  reminders are due, a stable machine-readable reason (`delivery_disabled`, `no_endpoint`,
  `endpoint_unreadable`, `endpoint_invalid`, `private_address_blocked`, `logs_unreadable`,
  `predictions_suppressed`, `no_reminder_kind`, `already_sent`, `not_due`, `ok`, plus
  `delivery_failed`/`claim_lost` from a real pass), and the next actionable fix in words —
  or the same facts as a versioned `ovumcy-notify-preflight/v1` JSON document. When several
  blocks apply the first one in that order wins, and a `--owner` filter that matches
  nothing is an error rather than an empty, healthy-looking table. Neither form prints a
  URL, path, query, userinfo, token, response body, reminder type or estimated date, so
  the output is safe to capture in an operator log.

### Internal

- **The notify pass records a per-owner outcome without changing what it does.** Each
  branch `processOwner` already took now also writes an outcome to a per-pass recorder —
  only when one is attached, and never to the service — so the CLI's diagnostic comes from
  the same decisions a delivery pass makes instead of a parallel walk that could drift.
  Delivery, claims, marks, watermarks, the aggregate counters and the existing log lines
  are unchanged; `RunOnce` and `NotifyPreflight` share one `run`.