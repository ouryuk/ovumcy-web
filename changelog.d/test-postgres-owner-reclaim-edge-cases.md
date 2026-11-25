none

Focused test-only coverage for Postgres test-container reclamation: unknown or
uninspectable owner identities are preserved, and repeated reclaim attempts
remain safe when an orphaned container has already disappeared. These cases
protect the owner-role work from treating missing ownership as proof of
orphaned state.
