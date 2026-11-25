none

Test-only: the day editor's cycle-factor chips now have their own coverage in both packages
that feed them, so an edit to either half fails by name instead of degrading silently.

In `internal/services`, the resolvers behind the chip — `DayCycleFactorTranslationKey` and
`DayCycleFactorIcon` — were reachable only through a template render, and nothing held them
against `SupportedDayCycleFactorKeys`, the list the editor and the stats surfaces range over.
The new tests pin the list against the model constants, prove the accessor hands out a copy
(editing the returned slice would reorder every stored row, because normalization reads the
package list for its stable order), require every supported key to resolve to a distinct label
key in the `dashboard.cycle_factors.` namespace and to its own icon, and hold both resolvers to
the spellings normalization accepts (`" Travel "`, `"TRAVEL"`) with the bullet and the raw key
as the unknown-key fallbacks. The stored factor order is pinned to the advertised one whatever
order the browser posts the checkboxes in.

In `internal/i18n`, the locale parity sweep only guarantees every catalogue carries the same
key set, not that the set a chip needs is in it: `translateMessage` falls back to the key
itself, so a factor missing from one locale renders the literal
`dashboard.cycle_factors.stress` inside a translated page. Every factor key is now required in
every loaded catalogue, non-empty and not an echo of the key. No production code, no behaviour
change.