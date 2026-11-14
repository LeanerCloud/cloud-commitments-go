package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ClaimPhase is what a purchase claim protects at a given moment (go#745).
// A caller's storage CHECK constraint must use exactly these strings.
type ClaimPhase string

const (
	// ClaimClaimed: the holder owns the token and no purchase request has been
	// sent. No money moved.
	ClaimClaimed ClaimPhase = "claimed"
	// ClaimSending: set immediately before the purchase request. From here the
	// provider may hold an order even if the holder dies.
	ClaimSending ClaimPhase = "sending"
	// ClaimCompleted: an order exists and its ID is recorded. Terminal.
	ClaimCompleted ClaimPhase = "completed"
)

// Default lease numbers (go#745 plan Q3). The settle window must exceed the
// lease TTL plus the client-side request bound plus the provider's listing
// propagation delay.
const (
	DefaultClaimTTL           = 120 * time.Second
	DefaultClaimRenewInterval = DefaultClaimTTL / 3
	DefaultClaimSettleWindow  = 10 * time.Minute
)

var (
	// ErrClaimHeld is returned by AcquireClaim when another holder's lease is
	// live. Nothing was changed.
	ErrClaimHeld = errors.New("purchase claim held by another holder")
	// ErrClaimSettlePending is returned by AcquireClaim when the lease on a
	// sending claim has expired but the settle window since the holder's last
	// renewal has not elapsed. The earlier holder's purchase may still commit,
	// so it also wraps ErrOutcomeUnknown (go#745 plan Q4: fail loud, buy nothing).
	ErrClaimSettlePending = fmt.Errorf("purchase claim settle window has not elapsed: %w", ErrOutcomeUnknown)
	// ErrClaimLost is returned by RenewClaim, AdvanceClaim and ReleaseClaim when
	// the row is no longer this holder and generation.
	ErrClaimLost = errors.New("purchase claim lost")
)

// Claim is the handle AcquireClaim returns. Holder and Generation identify the
// lease (Generation is the fencing token); Phase is the row's phase at
// acquisition and OrderID is set only when Phase is ClaimCompleted.
type Claim struct {
	Token      string
	Holder     string
	Phase      ClaimPhase
	OrderID    string
	Generation int64
}

// ClaimOutcome tells ReleaseClaim how the claim ended. OrderID non-empty means
// an order exists: the row becomes completed. Empty means nothing was bought
// by the holder: the row is deleted, but only if the store still has it in
// phase claimed. A sending row is left to expire whatever the holder saw.
type ClaimOutcome struct {
	OrderID string
}

// PurchaseClaimer is an optional interface an AttemptJournal may also
// implement (go#745). A provider client finds it by type assertion on
// PurchaseOptions.AttemptJournal; a journal without it behaves as before.
// It makes a purchase exclusive per idempotency token ACROSS processes, which
// the AttemptJournal record alone does not.
//
// Every duration is relative and every instant is computed by the store, never
// by the caller, so replica clock skew cannot change a lease.
//
// AcquireClaim, as one atomic step:
//   - no row: insert with generation 1, phase claimed, expiry now+ttl.
//   - phase completed: change nothing; return a Claim with Phase completed and
//     the recorded OrderID, and no error.
//   - live lease: change nothing; return an error wrapping ErrClaimHeld.
//   - expired claimed row: take over, generation+1, phase claimed.
//   - expired sending row: if the last renewal plus settle has not elapsed,
//     change nothing and return an error wrapping ErrClaimSettlePending;
//     otherwise take over, generation+1, KEEP phase sending, so a later peer
//     cannot skip the settle window.
//
// RenewClaim extends the lease and records the renewal time. AdvanceClaim moves
// claimed to sending (sending to sending is a no-op); no other phase is
// accepted. ReleaseClaim ends the claim per ClaimOutcome. Renew, Advance and
// Release return an error wrapping ErrClaimLost when the row is no longer
// (Holder, Generation). Any other error means the state is not durably known.
type PurchaseClaimer interface {
	AcquireClaim(ctx context.Context, token, holder string, ttl, settle time.Duration) (Claim, error)
	RenewClaim(ctx context.Context, claim Claim, ttl time.Duration) error
	AdvanceClaim(ctx context.Context, claim Claim, phase ClaimPhase) error
	ReleaseClaim(ctx context.Context, claim Claim, outcome ClaimOutcome) error
}
