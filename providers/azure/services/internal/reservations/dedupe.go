package reservations

import "github.com/LeanerCloud/cloud-commitments-go/pkg/common"

// DedupeCommitmentsByID collapses repeated commitments down to at most one
// per CommitmentID, keeping the first occurrence.
//
// The armconsumption.ReservationsDetails API these clients page through is a
// daily usage API: it returns one row per reservation per usage day (see
// ReservationDetailProperties.UsageDate), not a reservation inventory. With
// no filter on that date, a single reservation held for N days produces N
// common.Commitment values sharing one CommitmentID. Any caller that sums
// existing commitments for coverage, or dedupes recommendations against
// them, would then see roughly Nx the real committed capacity and suppress
// purchases that should happen (issue #73).
//
// A commitment with an empty CommitmentID cannot be deduped by identity and
// is always kept, since dropping it on an empty-string collision would
// silently discard unrelated reservations rather than just the intended
// daily-duplicate rows.
func DedupeCommitmentsByID(commitments []common.Commitment) []common.Commitment {
	seen := make(map[string]bool, len(commitments))
	result := make([]common.Commitment, 0, len(commitments))
	for i := range commitments {
		c := &commitments[i]
		if c.CommitmentID == "" {
			result = append(result, *c)
			continue
		}
		if seen[c.CommitmentID] {
			continue
		}
		seen[c.CommitmentID] = true
		result = append(result, *c)
	}
	return result
}
