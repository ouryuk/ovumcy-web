### Fixed

- **`ovumcy trends` no longer reports half a phase breakdown.** The command is the one
  phase-analytics surface that does not gate on the pattern threshold — it reports the
  completed-cycle count even for two cycles, so an operator reads what those cycles
  actually recorded instead of an empty answer. But the two halves of the breakdown were
  two independent decisions: the mood averages were computed unconditionally while the
  symptom counts still rode the threshold the stats page uses. An owner with two completed
  cycles therefore got a document that reported each phase's average mood while refusing
  to report the symptoms logged on the very same days — and the operator could not tell an
  empty phase from a withheld one. Both halves are now computed from the same completed
  cycle contexts under the same rules, so a phase with nothing in it is reported as empty
  rather than withheld. The two surfaces agree from the pattern minimum upward, because
  they share the counting. The stats page is unchanged: it still claims a pattern only from
  three completed cycles.
