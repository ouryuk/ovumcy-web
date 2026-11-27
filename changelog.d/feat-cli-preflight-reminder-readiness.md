### Added

- **`ovumcy preflight` answers "will this owner actually receive their next reminder?" without
  sending anything.** The webhook notify pass, `webhook show` and `predict` each held one half of the
  answer, and the whole question — "the reminders are not arriving, is it us?" — needed an operator
  to correlate three commands by hand. The new read-only CLI subcommand
  (`ovumcy preflight <email> [--id <id>] [--tz <IANA name>] [--json]`) composes what those commands
  already derive into one readiness report: the next reminder kind and its projected date, whether
  the owner's webhook is configured and enabled, the last accepted delivery stamp and its age, and
  an ordered list of blocking findings, each with a one-line fix. The findings cover no endpoint
  configured, delivery switched off, both reminder kinds switched off, no delivery ever recorded, a
  stale last delivery, predictions withheld by the suppression gate (unpredictable cycle, pregnancy
  pause, overdue cycle, or awaiting a first completed cycle), no cycle history to project from, and a
  published projection that produced no date.

  It is strictly read-only in the strong sense: it makes no outbound request — no webhook is sent, no
  endpoint is dialled, and the stored endpoint is never decrypted, because the command holds no
  `SECRET_KEY` — and it writes nothing, moving no watermark and stamping no delivery mark, so asking
  the question never changes the answer. The "due today" field is the notify pass's own
  `services.DecideDueReminders` called unchanged (it was already a pure function), so the report
  cannot disagree with what a real pass would send, and the projected dates come from the same
  `BuildCycleStatsFromLogs` → `PublishedStats` → `DashboardUpcomingPredictions` path `predict` and the
  notify pass use.

  Exit codes are a documented contract: **0** when nothing blocks the next reminder, **1** when at
  least one blocking finding is reported (the report is still written to stdout first), and non-zero
  from the usual error path for operational failures, so a monitoring check can rely on the code
  while an interactive operator reads the report. `--json` emits `ovumcy-preflight/v1`. Whether a
  stored endpoint is still decryptable after a `SECRET_KEY` rotation stays `webhook show`'s job — the
  one command that holds the key — and `preflight` points an operator there rather than guessing.
