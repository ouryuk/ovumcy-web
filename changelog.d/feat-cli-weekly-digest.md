### Added

- **`ovumcy digest` composes a shareable weekly digest.** The read-only CLI family
  (`summary`, `trends`, `predict`) answered one question at a time, each in a terminal
  rendering an operator could not hand to anyone. The new subcommand
  (`ovumcy digest <email> [--id <id>] [--week] [--days N] [--format markdown|json]
  [--tz <IANA name>] [--ntfy]`) composes the three shipped derivations — the summary's
  adherence, symptom tallies and mood days, the trends document's per-phase patterns, and
  the predict document's next-cycle projection — into one Spanish-language Markdown digest,
  or the same numbers as a versioned `ovumcy-digest/v1` JSON document. The window resolves
  in the owner's persisted timezone, so "this week" means the owner's week, not the server's.
  `--ntfy` opts the digest into delivery through the owner's already-stored webhook, so no
  new egress target is introduced and nothing is written when the flag is absent. A
  pregnancy pause suppresses the projection and an owner with no cycle history says so, as
  in `predict`; every rendering carries the projection disclaimer.
