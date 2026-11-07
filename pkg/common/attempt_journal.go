package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

import (
	"context"
	"errors"
	"fmt"
)

// AttemptState is the lifecycle state of one purchase attempt. A caller's
// storage CHECK constraint must use exactly these strings.
type AttemptState string

const (
	AttemptSending        AttemptState = "sending"
	AttemptPurchased      AttemptState = "purchased"
	AttemptOutcomeUnknown AttemptState = "outcome_unknown"
)

// AttemptRecord is one durable entry of an AttemptJournal.
type AttemptRecord struct {
	IdempotencyToken string
	ExecutionID      string
	State            AttemptState
	// ReservationID is the provider-side commitment ID; set only when State is
	// AttemptPurchased.
	ReservationID string
	// OfferingID is the offering the attempt was sent under; write-once.
	OfferingID string
}

// Validate reports whether the record is well formed. Error text never
// includes the full idempotency token.
func (r AttemptRecord) Validate() error {
	if r.IdempotencyToken == "" {
		return errors.New("attempt record: idempotency token is required")
	}
	if r.ExecutionID == "" {
		return fmt.Errorf("attempt record %s: execution ID is required", MaskToken(r.IdempotencyToken))
	}
	switch r.State {
	case AttemptSending, AttemptOutcomeUnknown:
		if r.ReservationID != "" {
			return fmt.Errorf("attempt record %s: reservation ID is only valid in state %q, got %q",
				MaskToken(r.IdempotencyToken), AttemptPurchased, r.State)
		}
	case AttemptPurchased:
		if r.ReservationID == "" {
			return fmt.Errorf("attempt record %s: state %q requires a reservation ID",
				MaskToken(r.IdempotencyToken), AttemptPurchased)
		}
	default:
		return fmt.Errorf("attempt record %s: unknown state %q", MaskToken(r.IdempotencyToken), r.State)
	}
	return nil
}

// AttemptJournal is a durable record of purchase attempts per idempotency
// token (go#107). It is a record, not a lock.
//
// RecordAttempt is committed before it returns. Allowed transitions:
// none to sending or purchased; sending to purchased or outcome_unknown;
// outcome_unknown to purchased; purchased to purchased only with the same
// ReservationID. Everything else is an error, including an identical
// sending or outcome_unknown repeat. OfferingID is write-once. Any error from
// either method means the state is not durably known. Implementations should
// call AttemptRecord.Validate before storing.
//
// Caller contract: a caller that re-dispatches a token whose earlier attempt
// may have been sent must first wait long enough for the provider's listing to
// show a commitment that attempt bought.
type AttemptJournal interface {
	// PriorAttempts returns the records of every execution for the token,
	// including the caller's own; none is not an error.
	PriorAttempts(ctx context.Context, token string) ([]AttemptRecord, error)
	RecordAttempt(ctx context.Context, rec AttemptRecord) error
}

// ClaimReader is an optional interface an AttemptJournal may also implement
// (go#700). A provider client finds it by type assertion on
// PurchaseOptions.AttemptJournal; a journal without it behaves as before.
//
// ClaimedByOthers returns the subset of ids that some attempt under a token
// other than token has recorded as purchased. An id that only the given token
// recorded is not returned, and an empty result excludes nothing. Any error
// means the claims are not known.
type ClaimReader interface {
	ClaimedByOthers(ctx context.Context, token string, ids []string) ([]string, error)
}
