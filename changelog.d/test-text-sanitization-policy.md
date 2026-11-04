none

Test-only: the two text-sanitization policies of `internal/services` now have direct unit tests.
The CSV formula-injection guard (`sanitizeCSVTextCell` and the predicate it reads,
`hasDangerousCSVPrefix`) and the plain-text label guard (`containsInvalidPlainTextLabelRune`)
were reachable only through their callers, so nothing pinned which leading characters the CSV
guard treats as a formula, that an all-space value is safe, or that the label guard refuses a
control rune and a markup delimiter. The new file drives each helper on its own, including the
space-padded disguise a spreadsheet skips and the positive controls that keep the guard from
quoting every cell. No production code, no behaviour change.
