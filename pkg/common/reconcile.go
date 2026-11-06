package common

import "time"

// ReconcileVerdict is the outcome of ReconcilePurchase. There is deliberately no NotFound value: a lookup
// can never prove a purchase did not happen (describe lag, clock drift, modified or split commitments), so
// "nothing found" is Unknown. The zero value is not Found.
type ReconcileVerdict string

const (
	ReconcileUnknown ReconcileVerdict = "unknown"
	ReconcileFound   ReconcileVerdict = "found"
)

// ReconcileRequest describes a purchase attempt that returned ErrOutcomeUnknown.
type ReconcileRequest struct {
	// AttemptEndedAt is when that attempt returned ErrOutcomeUnknown. Required: it closes the match window.
	AttemptEndedAt time.Time
	// Options are the PurchaseOptions of the attempt that returned ErrOutcomeUnknown. RequestedAt (the
	// lineage's first dispatch) is required; IdempotencyToken and OfferingClass are used when set.
	Options        PurchaseOptions
	Recommendation Recommendation
}

// ReconcileResult is the read-only answer to "did that purchase land?".
type ReconcileResult struct {
	Verdict      ReconcileVerdict
	CommitmentID string   // set only with ReconcileFound
	Reason       string   // Unknown only: why, one fixed phrase per branch
	Criteria     []string // fields that matched, e.g. "instance-type", "count", "start-window"
	CandidateIDs []string // Unknown only: commitments that kept the answer open (operator checks these)
	Tagged       bool     // Found carried the request's idempotency token tag (EC2 only)
}

// IsFound is true only for a Found verdict that names a commitment.
func (r ReconcileResult) IsFound() bool {
	return r.Verdict == ReconcileFound && r.CommitmentID != ""
}
