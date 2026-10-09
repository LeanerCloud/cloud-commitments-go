# Known Issues: GCP Compute Engine CUD term on the non-root payload path

> **Audit status (2026-10-09):** `1 needs triage · 0 resolved`

## MEDIUM: Non-root recommendation payloads still guess the term

**Files:** `providers/gcp/services/computeengine/commitment_type.go`
(`resolveRecTerm`, `populateRecommendationResources`)

**Description:** The fix for #291 reads the commitment `plan` from the root
Commitment operation. A recommendation with no root Commitment op has no plan
to read, so its term still comes from `params.Term`, defaulting to "1yr".

**Why deferred:** The Recommender payload shape for non-root operations is
unverified, and #291 was scoped to the root path. Tracked in #304.

**Status:** Open.
