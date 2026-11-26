none

Test-only change: the sex-activity, cervical-mucus and pregnancy-test
enumerations in `internal/services/day_tracking.go` had their IsValid*/Normalize*
pair exercised only through `NormalizeDayEntryInput`, with an exact constant or
a wrong value. `day_tracking_enum_test.go` now pins the pair directly — every
accepted spelling (mixed case, whitespace-padded, the empty "not recorded")
canonicalises to its stored constant, each constant is a fixed point, and every
unknown value is both refused by the gate and neutralised to "none" by the
normaliser the import and merge paths call without it.
