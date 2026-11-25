none

Test-only: the egress ledger's own state machine in `internal/services` now has direct unit
tests. `resolveIntrinsicWebhookState`, `applyOutboundDeliveryOverlay`, `resolveFeedState`,
`resolveEgressSectionState`, `webhookHostForState`, `deliveryEvidenceForState` and
`revealEvidenceForState` are the pure resolvers behind `BuildEgressLedger`, and they were
reachable only through it, so each branch was exercised only insofar as one of the handful of
ledger builds happened to pass through it. A branch no build reached had no test of its own:
reorder the switch arms, invert one comparison or delete one case, and the ledger still produced
a plausible ledger for every fixture that existed.

The new file asks each resolver directly, over the package's own enumerations of webhook, feed
and heading states, so the property under test is the precedence the state machine claims rather
than the shape of the ledger one particular row happens to produce: readability outranking every
toggle, the instance-wide delivery switch speaking only for the three states that ARE delivery
configuration, the feed machine distinguishing "no feed" from "a feed whose row could not be
read", the heading reading the intrinsic webhook state so an armed row is not reported as quiet
on an instance that ships the scheduler off, and the delivery and reveal marks surviving only in
the states that can vouch for the endpoint and the link they describe.

One enumerated webhook state is deliberately not reachable from the row's own state:
`outbound_disabled` is an instance fact laid over the row by the overlay, and the test excludes
it by name rather than by count. No production code, no behaviour change.
