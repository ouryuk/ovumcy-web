### Features

- **`ovumcy predict` projects the next cycle window.** The dashboard and the calendar feed already
  project the next period start, ovulation date and fertile window, but only behind a login; an
  operator had no way to answer "what is this account's projected window as of today?" from the
  shell. The new read-only CLI subcommand (`ovumcy predict <email> [--id <id>] [--tz <IANA name>]
  [--json]`) runs the same `BuildCycleStatsFromLogs` derivation those surfaces use and prints the
  current cycle day, phase and fertility status, the basis the projection was computed from
  (completed cycles, median and predicted lengths, personalised luteal phase), then the projected
  next period start, ovulation date and fertile range — or the same numbers as a versioned
  `ovumcy-predict/v1` JSON document. A pregnancy pause suppresses the projection explicitly, an
  owner with no cycle history says so, and every rendering carries the projection disclaimer.
  Nothing is written and no secret is read, exactly like `export`, `summary` and `trends`.
