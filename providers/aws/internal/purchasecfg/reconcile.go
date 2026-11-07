package purchasecfg

import (
	"slices"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// ClockSkew pads both ends of the reconcile match window (equals the Redshift guard's skew).
const ClockSkew = 5 * time.Minute

// One fixed Reason phrase per Unknown branch; callers never invent their own.
const (
	ReasonInvalidWindow      = "invalid-window"
	ReasonInvalidRequest     = "invalid-request"
	ReasonLookupFailed       = "lookup-failed"
	ReasonLookupIncomplete   = "lookup-incomplete"
	ReasonOfferingUnresolved = "offering-unresolved"
	ReasonNoCandidate        = "no-candidate"
	ReasonAmbiguous          = "ambiguous"
	ReasonUncertainCandidate = "uncertain-candidate"
)

// ReconcileWindow returns the padded interval a candidate's start time must fall in.
// ok is false for a zero time or an end before start.
func ReconcileWindow(start, end time.Time) (lo, hi time.Time, ok bool) {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return time.Time{}, time.Time{}, false
	}
	return start.Add(-ClockSkew), end.Add(ClockSkew), true
}

// InWindow reports whether t lies in [lo, hi], both ends inclusive.
func InWindow(t, lo, hi time.Time) bool {
	return !t.Before(lo) && !t.After(hi)
}

// UnknownResult is the verdict for early exits and failures.
func UnknownResult(reason string) common.ReconcileResult {
	return common.ReconcileResult{Verdict: common.ReconcileUnknown, Reason: reason}
}

// Decide is the single fail-closed rule for both services: Found only for exactly one match and no
// uncertain candidate.
func Decide(matches, uncertain, criteria []string, tagged bool) common.ReconcileResult {
	// A match without an ID cannot be reported as Found, so it counts as uncertain.
	var ids []string
	blank := false
	for _, id := range append(append([]string{}, matches...), uncertain...) {
		if id == "" {
			blank = true
			continue
		}
		ids = append(ids, id)
	}
	if blank || len(uncertain) > 0 {
		res := UnknownResult(ReasonUncertainCandidate)
		res.CandidateIDs = ids
		return res
	}
	switch len(matches) {
	case 0:
		return UnknownResult(ReasonNoCandidate)
	case 1:
		return common.ReconcileResult{
			Verdict:      common.ReconcileFound,
			CommitmentID: matches[0],
			Criteria:     slices.Clone(criteria),
			Tagged:       tagged,
		}
	default:
		res := UnknownResult(ReasonAmbiguous)
		res.CandidateIDs = append([]string{}, matches...)
		return res
	}
}
