### Dependencies

- Patch-level Go module bumps only (same minor line): `github.com/go-jose/go-jose/v4`
  4.1.8 → 4.1.9 (the JOSE implementation behind the identity-provider login),
  `github.com/jackc/puddle/v2` 2.2.3 → 2.2.4 (the connection pool the Postgres driver
  sits on), and `github.com/shamaton/msgpack/v3` 3.2.6 → 3.2.8 (a module-graph floor;
  no package from it is linked into the shipped binary). All three are indirect
  requirements, so only `go.mod`/`go.sum` changed and no migration is implied.
  Minor/major updates seen in the graph (`gofiber/fiber/v3` 3.7.0, `pgx/v5` 5.13.0,
  `golang.org/x/*`, `modernc.org/*`, …) were deliberately skipped.
