-- Postgres mirror of migrations/040_webhook_deliveries.sql (owner-visible
-- webhook delivery history). Same version number and basename so schema
-- history stays aligned across engines.
--
-- The table is a RING: it keeps the LATEST 20 rows per owner and prunes the
-- rest on insert. The bound is per owner (user_id), not global.
--
-- endpoint_host is the DESTINATION HOSTNAME ONLY -- never the full URL, its
-- path, its query string, or any token embedded in its userinfo or query.
-- error_class is a bounded enum (timeout, dns, tls, refused_redirect,
-- private_address, http_error, transport) naming the KIND of failure and
-- never the error text, because a transport error message can embed the URL.
-- http_status is 0 when there was no response at all.
--
-- The index is (user_id, occurred_at DESC) -- the one read this table serves:
-- the newest rows for one owner, newest first.
--
-- The rows are erased with the account: DeleteAccountAndRelatedData deletes
-- every user-scoped table explicitly, so erasure completeness does not depend
-- on the foreign key.
--
-- CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS keep the file
-- idempotent across the postgres test bootstrap and rolling deploys, in
-- addition to the runner's own already-applied skip. Rollback (forward-only
-- repo) is documented in the commit body, not here.
--
-- NOTE: keep prose in this file free of semicolons -- the migration runner
-- splits statements on the semicolon character without stripping SQL comments.

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL,
  reminder_kind TEXT NOT NULL,
  outcome TEXT NOT NULL,
  http_status INTEGER NOT NULL DEFAULT 0,
  error_class TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  endpoint_host TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_user_occurred_at
  ON webhook_deliveries (user_id, occurred_at DESC);
