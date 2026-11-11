none

Internal only: `internal/models` coverage had drifted into a broken state —
the auto-merged consolidated `models_test.go` redeclared
`TestDailyLogBeforeSave` and `TestSymptomTypeIsActive`, which already lived in
the split per-model test files, so the package did not compile. The duplicate
declarations are gone; every unique assertion survives: the built-in symptom
catalog (entries, defensive copy, key uniqueness), `DailyLog.BeforeSave`
(date canonicalization, nil/non-nil cycle-factor keys, key order), the four
pinned table names plus proof that gorm's own naming strategy would derive
three of them differently, the cycle-factor wire constants, and the two shared
error sentinels with their `errors.Is` identity (ported from the scout
branch that first wrote them). No production code changed.
