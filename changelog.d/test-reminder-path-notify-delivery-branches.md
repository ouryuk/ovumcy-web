none

Test-only change, with no user-visible effect: the reminder delivery path's
remaining untested branches are now pinned. The notify pass reports one
skipped-already-sent per watermarked kind (two when both period and ovulation
are covered) and claims nothing for either; each kind's claim is compared
against its own watermark slot, never the sibling kind's; a failed POST releases
the claim into the matching kind's slot. The hardened deliverer falls through
to the next resolved address when the first refuses the connection, sweeps every
address for privacy before the first dial, and names the refused-redirect and
blocked-private-address reasons rather than a generic transport error. The
owner-initiated test notification treats an endpoint that decrypts to nothing as
not configured, dates its payload on the owner's persisted timezone, and sends
the headline alone when the catalogue has no message.
