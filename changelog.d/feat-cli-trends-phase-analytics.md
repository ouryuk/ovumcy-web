### Added

- **`ovumcy trends` correlates symptoms and moods with cycle phases.** The stats page's phase
  insights lived only behind a login; an operator had no way to ask "which phase do this owner's
  headaches cluster in?" without opening the app. The new read-only CLI subcommand
  (`ovumcy trends <email> [--id <id>] [--tz <IANA name>] [--json]`) recomputes the same
  completed-cycle phase contexts — menstrual / follicular / ovulation / luteal — over the owner's
  full log history and prints one phase per line with its symptom counts, percentages and average
  mood, or the same numbers as a versioned `ovumcy-trends/v1` JSON document. Unlike the stats page
  it reports the completed-cycle count even below the pattern threshold, so "2 cycles, no pattern
  claimed" is visible instead of an empty answer. Nothing is written and no secret is read, exactly
  like `export` and `summary`.
