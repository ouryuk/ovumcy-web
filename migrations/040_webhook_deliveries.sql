-- Webhook delivery history (owner-visible): one row per delivery attempt, so
-- an owner can see what actually happened to their reminders instead of
-- guessing from a single "last delivered" mark.
--
-- The table is a RING: it keeps the LATEST 20 rows per owner and prunes the
-- rest on insert. The bound is per owner (user_id), not global, so a busy
-- instance cannot evict another owner's history, and the pruning happens in the
-- INSERT path so no background job is needed.
--
-- WHAT IS STORED, and what is deliberately NOT:
--
--   * endpoint_host is the DESTINATION HOSTNAME ONLY. The full URL -- its
--     path, its query string and any token embedded in its userinfo or query
--     (ntfy and Gotify both put the secret there) -- is NEVER written here.
--     The same hostOnly redaction the egress surfaces use is the only rule
--     applied, so one hardening reaches every surface at once.
--   * error_class is a BOUNDED ENUM (timeout, dns, tls, refused_redirect,
--     private_address, http_error, transport). It names the KIND of failure
--     and never the error text: a transport error message can embed the URL it
--     was raised for, and the URL can carry the token.
--   * http_status is the endpoint's own status code, or 0 when there was no
--     response at all (a timeout, a refused dial, a DNS failure).
--   * latency_ms is the measured round-trip in milliseconds.
--
-- The rows carry NO health specifics: a reminder row names the KIND of
-- reminder (period, ovulation, test) and nothing about the cycle it was about.
-- The predicted date the reminder carried stays in the payload that left the
-- instance, not in this table.
--
-- The index is (user_id, occurred_at DESC) -- the one read this table serves:
-- the newest rows for one owner, newest first. It is the ring's own read path,
-- so the pruning statement uses it too.
--
-- The rows are erased with the account: the table carries user_id, and
-- DeleteAccountAndRelatedData deletes every user-scoped table explicitly, so
-- erasure completeness holds without relying on the foreign key.
--
-- Idempotency: CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS keep
-- the file idempotent across clean installs and rolling deploys, in addition
-- to the runner's own already-applied skip. Rollback (forward-only repo) is
-- documented in the commit body, not here.
--
-- NOTE: keep prose in this file free of semicolons -- the migration runner
-- splits statements on the semicolon character without stripping SQL comments,
-- so a semicolon inside a comment is mis-parsed as a statement boundary.

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  occurred_at DATETIME NOT NULL,
  reminder_kind TEXT NOT NULL,
  outcome TEXT NOT NULL,
  http_status INTEGER NOT NULL DEFAULT 0,
  error_class TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  endpoint_host TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_user_occurred_at
  ON webhook_deliveries (user_id, occurred_at DESC);
