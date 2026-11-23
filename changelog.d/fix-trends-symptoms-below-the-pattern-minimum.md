### Fixed

- **`ovumcy trends` no longer hides an owner's symptoms below the stats page's pattern
  threshold.** The document reports the completed-cycle count beside whatever it could
  measure — "2 completed cycles, no pattern claimed" instead of an empty answer — and its
  mood half has always been counted over the cycles the owner actually has. The symptom
  half kept the stats page's minimum of three completed cycles, so a two-cycle account
  was shown the average mood of its cycle phases with every phase's symptoms silently
  absent. That reads as "there were no symptoms in that phase", a fact the owner knows to
  be false, rather than "there were too few cycles to count them". Both halves are now
  counted over the same cycles and agree with the stats page from the pattern minimum
  upward; the stats page's own threshold is unchanged.
