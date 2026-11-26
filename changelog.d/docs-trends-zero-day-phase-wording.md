none

This branch only sharpens the operator documentation for `ovumcy trends`: README.md
and docs/self-hosted.md now spell out that both halves of the phase breakdown are
counted over whatever completed cycles the owner has, with no pattern minimum, so a
phase whose JSON shows `total_days` 0 — or that shows no numbers beside its name in
the table — recorded neither a symptom nor a mood and is empty rather than withheld.

No code changed. The behaviour the wording describes reached the release notes with
`fix-trends-reports-both-halves.md`, which is the fragment for the fix itself; a
second entry here would document the same release-notes line twice.
