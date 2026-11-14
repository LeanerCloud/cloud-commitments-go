package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

import (
	"context"
	"errors"
	"testing"
	"time"
)

type claimSuite struct {
	t   *testing.T
	c   PurchaseClaimer
	adv func(time.Duration)
}

const (
	suiteToken  = "conformance-token"
	suiteTTL    = 10 * time.Second
	suiteSettle = 100 * time.Second
)

var suiteCtx = context.Background()

// RunPurchaseClaimerConformance checks the PurchaseClaimer rules (go#745) against
// any implementation, so a durable store can be held to the in-memory reference.
// newClaimer returns a fresh store and a function that advances the store's own
// clock; a store with database time implements it by scaling or sleeping.
func RunPurchaseClaimerConformance(t *testing.T, newClaimer func(t *testing.T) (c PurchaseClaimer, advance func(time.Duration))) {
	t.Helper()
	cases := []struct {
		run  func(*claimSuite)
		name string
	}{
		{(*claimSuite).take, "take: fresh token is claimed at generation 1, a live lease conflicts"},
		{(*claimSuite).renew, "renew: extends the lease past the original expiry"},
		{(*claimSuite).advance, "advance: claimed to sending, repeat is a no-op, other phases are refused"},
		{(*claimSuite).releaseOrder, "release: an order completes the row and later acquires see it"},
		{(*claimSuite).releaseClaimedDeletes, "release: nothing bought deletes a claimed row"},
		{(*claimSuite).releaseSendingKept, "release: nothing bought keeps a sending row"},
		{(*claimSuite).staleHandles, "fence: a stale handle fails after delete and re-insert, and for the same holder at an older generation"},
		{(*claimSuite).takeover, "takeover: an expired claimed row moves to generation 2 and fences the old holder"},
		{(*claimSuite).settle, "settle: an expired sending row refuses takeover until the window passes, then keeps sending"},
		{(*claimSuite).settleFromRenewal, "settle: the window runs from the last renewal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, adv := newClaimer(t)
			tc.run(&claimSuite{t: t, c: c, adv: adv})
		})
	}
}

func (s *claimSuite) acquire(holder string) Claim {
	s.t.Helper()
	cl, err := s.c.AcquireClaim(suiteCtx, suiteToken, holder, suiteTTL, suiteSettle)
	if err != nil {
		s.t.Fatalf("acquire as %s: %v", holder, err)
	}
	return cl
}

func (s *claimSuite) acquireErr(holder string, want error) {
	s.t.Helper()
	_, err := s.c.AcquireClaim(suiteCtx, suiteToken, holder, suiteTTL, suiteSettle)
	s.wantErr(err, want)
}

func (s *claimSuite) wantErr(err, want error) {
	s.t.Helper()
	if !errors.Is(err, want) {
		s.t.Fatalf("got %v, want an error wrapping %v", err, want)
	}
}

func (s *claimSuite) must(err error) {
	s.t.Helper()
	if err != nil {
		s.t.Fatal(err)
	}
}

func (s *claimSuite) sending(holder string) Claim {
	s.t.Helper()
	a := s.acquire(holder)
	s.must(s.c.AdvanceClaim(suiteCtx, a, ClaimSending))
	return a
}

func (s *claimSuite) take() {
	a := s.acquire("a")
	if a.Generation != 1 || a.Phase != ClaimClaimed || a.Holder != "a" {
		s.t.Fatalf("got %+v", a)
	}
	s.acquireErr("b", ErrClaimHeld)
}

func (s *claimSuite) renew() {
	a := s.acquire("a")
	s.adv(8 * time.Second)
	s.must(s.c.RenewClaim(suiteCtx, a, suiteTTL))
	s.adv(8 * time.Second)
	s.acquireErr("b", ErrClaimHeld)
}

func (s *claimSuite) advance() {
	a := s.acquire("a")
	for i := 0; i < 2; i++ {
		s.must(s.c.AdvanceClaim(suiteCtx, a, ClaimSending))
	}
	for _, p := range []ClaimPhase{ClaimClaimed, ClaimCompleted} {
		if err := s.c.AdvanceClaim(suiteCtx, a, p); err == nil {
			s.t.Fatalf("advance to %q must be refused", p)
		}
	}
}

func (s *claimSuite) releaseOrder() {
	a := s.acquire("a")
	s.must(s.c.ReleaseClaim(suiteCtx, a, ClaimOutcome{OrderID: "order-1"}))
	s.adv(time.Hour)
	if b := s.acquire("b"); b.Phase != ClaimCompleted || b.OrderID != "order-1" {
		s.t.Fatalf("got %+v", b)
	}
}

func (s *claimSuite) releaseClaimedDeletes() {
	a := s.acquire("a")
	s.must(s.c.ReleaseClaim(suiteCtx, a, ClaimOutcome{}))
	if b := s.acquire("b"); b.Generation != 1 || b.Phase != ClaimClaimed {
		s.t.Fatalf("claimed row must be gone: %+v", b)
	}
}

func (s *claimSuite) releaseSendingKept() {
	a := s.sending("a")
	s.must(s.c.ReleaseClaim(suiteCtx, a, ClaimOutcome{}))
	s.acquireErr("b", ErrClaimHeld)
}

func (s *claimSuite) takeover() {
	a := s.acquire("a")
	s.adv(suiteTTL + time.Second)
	if b := s.acquire("b"); b.Generation != 2 || b.Phase != ClaimClaimed {
		s.t.Fatalf("got %+v", b)
	}
	s.wantErr(s.c.RenewClaim(suiteCtx, a, suiteTTL), ErrClaimLost)
	s.wantErr(s.c.AdvanceClaim(suiteCtx, a, ClaimSending), ErrClaimLost)
	s.wantErr(s.c.ReleaseClaim(suiteCtx, a, ClaimOutcome{OrderID: "x"}), ErrClaimLost)
}

func (s *claimSuite) settle() {
	s.sending("a")
	s.adv(suiteTTL + time.Second)
	s.acquireErr("b", ErrClaimSettlePending)
	s.acquireErr("b", ErrOutcomeUnknown)
	s.adv(suiteSettle)
	b := s.acquire("b")
	if b.Generation != 2 || b.Phase != ClaimSending {
		s.t.Fatalf("takeover must keep phase sending: %+v", b)
	}
	s.must(s.c.ReleaseClaim(suiteCtx, b, ClaimOutcome{}))
	s.adv(suiteTTL + time.Second)
	s.acquireErr("c", ErrClaimSettlePending)
}

func (s *claimSuite) settleFromRenewal() {
	a := s.sending("a")
	s.adv(8 * time.Second)
	s.must(s.c.RenewClaim(suiteCtx, a, suiteTTL))
	s.adv(80 * time.Second)
	s.acquireErr("b", ErrClaimSettlePending)
	s.adv(21 * time.Second)
	s.acquire("b")
}

func (s *claimSuite) staleHandles() {
	a := s.acquire("a")
	s.must(s.c.ReleaseClaim(suiteCtx, a, ClaimOutcome{}))
	b := s.acquire("b")
	s.wantErr(s.c.RenewClaim(suiteCtx, a, suiteTTL), ErrClaimLost)
	s.adv(suiteTTL + time.Second)
	a2 := s.acquire("b")
	s.wantErr(s.c.RenewClaim(suiteCtx, b, suiteTTL), ErrClaimLost)
	s.must(s.c.RenewClaim(suiteCtx, a2, suiteTTL))
}
