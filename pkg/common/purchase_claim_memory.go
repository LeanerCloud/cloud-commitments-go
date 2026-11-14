package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ClaimRow is a read-only copy of one MemoryPurchaseClaimer row.
type ClaimRow struct {
	ExpiresAt  time.Time
	RenewedAt  time.Time
	Holder     string
	Phase      ClaimPhase
	OrderID    string
	Generation int64
	Exists     bool
}

// MemoryPurchaseClaimer is an in-process PurchaseClaimer. It is the reference
// for the interface's rules and the store used by tests; it is not durable and
// does not make anything exclusive across processes. now is the store's clock,
// the only time source (callers pass durations, never instants).
type MemoryPurchaseClaimer struct {
	now  func() time.Time
	rows map[string]*ClaimRow
	mu   sync.Mutex
}

// NewMemoryPurchaseClaimer returns an empty store reading time from now.
func NewMemoryPurchaseClaimer(now func() time.Time) *MemoryPurchaseClaimer {
	return &MemoryPurchaseClaimer{now: now, rows: map[string]*ClaimRow{}}
}

// Row returns a copy of the token's row; Exists is false when none.
func (m *MemoryPurchaseClaimer) Row(token string) ClaimRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[token]; ok {
		c := *r
		c.Exists = true
		return c
	}
	return ClaimRow{}
}

func (m *MemoryPurchaseClaimer) AcquireClaim(_ context.Context, token, holder string, ttl, settle time.Duration) (Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	r, ok := m.rows[token]
	switch {
	case !ok:
		r = &ClaimRow{Phase: ClaimClaimed}
		m.rows[token] = r
	case r.Phase == ClaimCompleted:
		return Claim{Token: token, Phase: ClaimCompleted, OrderID: r.OrderID}, nil
	case r.ExpiresAt.After(now):
		return Claim{}, fmt.Errorf("%w: lease expires in %s", ErrClaimHeld, r.ExpiresAt.Sub(now))
	case r.Phase == ClaimSending && r.RenewedAt.Add(settle).After(now):
		return Claim{}, fmt.Errorf("%w: %s remain", ErrClaimSettlePending, r.RenewedAt.Add(settle).Sub(now))
	}
	r.Holder = holder
	r.Generation++
	r.ExpiresAt = now.Add(ttl)
	r.RenewedAt = now
	return Claim{Token: token, Holder: holder, Generation: r.Generation, Phase: r.Phase}, nil
}

// owned returns the row when claim is still its (holder, generation). Caller holds m.mu.
func (m *MemoryPurchaseClaimer) owned(claim Claim) (*ClaimRow, error) {
	r, ok := m.rows[claim.Token]
	if !ok || r.Phase == ClaimCompleted || r.Holder != claim.Holder || r.Generation != claim.Generation {
		return nil, fmt.Errorf("%w: token %s", ErrClaimLost, MaskToken(claim.Token))
	}
	return r, nil
}

func (m *MemoryPurchaseClaimer) RenewClaim(_ context.Context, claim Claim, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.owned(claim)
	if err != nil {
		return err
	}
	now := m.now()
	r.ExpiresAt = now.Add(ttl)
	r.RenewedAt = now
	return nil
}

func (m *MemoryPurchaseClaimer) AdvanceClaim(_ context.Context, claim Claim, phase ClaimPhase) error {
	if phase != ClaimSending {
		return fmt.Errorf("purchase claim: cannot advance to phase %q", phase)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.owned(claim)
	if err != nil {
		return err
	}
	r.Phase = ClaimSending
	return nil
}

func (m *MemoryPurchaseClaimer) ReleaseClaim(_ context.Context, claim Claim, outcome ClaimOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.owned(claim)
	if err != nil {
		return err
	}
	switch {
	case outcome.OrderID != "":
		r.Phase = ClaimCompleted
		r.OrderID = outcome.OrderID
	case r.Phase == ClaimClaimed:
		delete(m.rows, claim.Token)
	}
	return nil
}
