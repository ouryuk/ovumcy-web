### Dependencies

- `gorm.io/driver/postgres` 1.6.2 → 1.6.3 — the one direct production module in this
  group, and the Postgres driver the storage layer runs on. No API changed and no
  migration is implied.
- `github.com/jackc/puddle/v2` 2.2.2 → 2.2.3 — the connection pool the driver sits on.
- `github.com/andybalholm/brotli` 1.2.2 → 1.2.8 — the compression library in the
  dependency graph.
- `github.com/go-jose/go-jose/v4` 4.1.4 → 4.1.8 — the JOSE implementation behind the
  identity-provider login.
- `github.com/tinylib/msgp` 1.6.4 → 1.6.5 and `github.com/philhofer/fwd` 1.2.0 → 1.2.1 —
  the message-pack codec and the writer it is built on.
- `github.com/mattn/go-colorable` 0.1.15 → 0.1.16 — the colour helper in the dependency
  graph.
- `github.com/mattn/go-sqlite3` 1.14.22 → 1.14.52 and `github.com/shamaton/msgpack/v3`
  3.2.0 → 3.2.6 — module-graph floors only: no package from either is linked into the
  shipped binary, which reaches SQLite through the pure-Go `github.com/glebarez/sqlite`
  driver, so these two change the recorded requirement and nothing that runs.
- `github.com/kr/pretty` 0.3.0 → 0.3.1 was in the same group, but `go mod tidy` drops the
  requirement again: no package from it belongs to this module's build (it reaches the
  graph only as a test dependency of `github.com/jackc/pgx/v5`), so the graph still
  selects 0.3.0 and nothing in the binary changes.
