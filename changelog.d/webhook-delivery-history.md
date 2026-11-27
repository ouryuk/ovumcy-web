### Added

- **Settings now shows what actually happened to your reminders.** Under the webhook card
  there is a short list of the most recent delivery attempts — when each one happened, which
  reminder it was (period, ovulation, or a test message), and how it ended: delivered, refused
  by your endpoint, or failed. Until now the page could only say when the last *accepted*
  delivery happened, which is silence about every attempt after it; an owner whose ntfy server
  went quiet had no way to tell "it is not being sent" from "it is being sent and rejected".
- **When the newest attempt failed, an alert says so and since when** — "Reminders are not
  reaching your endpoint since …" — instead of leaving the owner to read the list and draw the
  conclusion. A failed attempt followed by a successful one is a retry that worked and shows no
  alert.
- **A failed attempt says why, in words.** A timeout, a hostname that does not resolve, a TLS
  handshake that did not verify, a redirect the client refuses to follow, a private-network
  address that was blocked, or a status code your endpoint answered with — a bounded class, not
  an error message.
- **The history is also available to scripts and the operator CLI.** `GET
  /api/v1/users/current/webhook/deliveries` returns the current owner's latest twenty attempts
  (owner-only; the owner id comes from the session, never from the request), and `ovumcy webhook
  show <email>` appends the latest three outcomes to its status output.
- **Nothing about your endpoint's secret is stored or shown.** A history row records the
  destination *host* only — never the URL, its path, its query string, or a token embedded in
  either — and never an error's text, which can embed the URL. The table keeps the latest twenty
  rows per owner and prunes older ones as new attempts are recorded, so it cannot grow without
  bound. Rows are erased with the account and with the data wipe.
