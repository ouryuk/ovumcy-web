none

The user-visible half of this branch — `ovumcy trends` counting symptoms below the
stats page's pattern minimum, so the document stops reporting a phase's mood while
withholding its symptoms — is the same fix `main` already landed as
`fix-trends-reports-both-halves.md`, from a parallel branch that reached it first.
Two fragments would have put the same entry in the release notes twice, so this
branch's one claim that entry did not yet carry — the two surfaces agree from the
pattern minimum upward because they share the counting — was folded into it rather
than repeated here. What this branch adds to the tree on its own is the regression
tests (`TestBuildCyclePhaseTrendsReportsSymptomsBelowThePatternMinimum` and
`TestBuildCyclePhaseTrendsAgreesWithTheStatsPageAboveThePatternMinimum`, plus the
operator-visible `TestRunTrendsCommandReportsSymptomsBelowThePatternMinimum`), which
are test-only.
