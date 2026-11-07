# Proposal: owner-initiated webhook test action

Status: prototype implemented on the settings egress card.

## Why

An owner can save a webhook endpoint, enable reminders, and then wait until the
next scheduled pass to find out whether the destination works. A typo, an expired
ntfy/Gotify token, a firewall change, or a revoked endpoint is otherwise silent
until a real reminder is lost. The existing dashboard banner cannot test this
path because it never leaves the instance, and the operator CLI is not available
to every owner.

A test action closes that feedback gap without weakening the egress contract: it
is owner-only, CSRF-protected, uses the stored endpoint rather than a value typed
into the form, and reuses the exact hardened transport the reminder pass uses.

## What

- `POST /api/v1/users/current/webhook/test` (OwnerOnly, CSRF-protected).
- Sends exactly one test notification to the currently stored URL.
- The payload is a dedicated `type: test` message with no period or ovulation
  prediction, no lead-day claim, and no health specifics.
- No settings, watermark, or delivery mark is read or written by the probe; it
  therefore works while reminder delivery is switched off.
- Success means the endpoint answered 2xx. Missing configuration, an unreadable
  ciphertext, and transport failure are distinct, localized outcomes.
- The settings card renders a **Send test notification** button when a host can
  be named, and replaces itself with the outcome so the feedback appears beside
  the control.
- The stored URL, its path, query, and any embedded token never appear in the
  response or in a log line.

## How

1. `services.WebhookNotifyService.SendTest` reuses the notify pass's decryptor,
   hardened `WebhookDeliverer`, and localized-copy provider. It builds a fixed
   test payload and returns stable, secret-free sentinels for not-configured,
   unreadable, and delivery-failed outcomes.
2. `api.SendTestWebhook` loads the authenticated owner, passes only id, stored
   ciphertext, timezone, and interface language, maps the sentinels to localized
   settings-form errors, and answers through the existing rebuilt-egress-card
   transport.
3. `bootstrap.BuildDependencies` wires the same notify service recipe into the
   handler, including the `WEBHOOK_BLOCK_PRIVATE_ADDRESSES` gate, so the test
   action cannot become a parallel or weaker client.
4. The settings template adds the test form and hint; the existing status island
   shows the success or error copy in the owner's language.
5. Tests cover service success, missing configuration, unreadable endpoint, and
   transport failure at the service seam, plus HTTP success, missing config,
   transport failure, CSRF refusal, payload shape, and secret-free responses.

## Next steps

- Consider a per-owner rate limit if test deliveries prove abusable on a shared
  instance; the action currently inherits the global request limits only.
- Decide whether a successful test should update the egress card's "last delivery
  accepted" evidence. This prototype deliberately does not: the mark currently
  means a reminder delivery was accepted, and a probe is a different event.
- Add the test action to the operator CLI (`ovumcy webhook test <email>`) only if
  operators need it; the web action already exercises the same transport.
- Carry the same explicit test payload shape into any future consumer
  documentation so `type: test` is not mistaken for a reminder kind.
