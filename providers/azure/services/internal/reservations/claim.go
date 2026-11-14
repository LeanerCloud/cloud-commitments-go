package reservations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// claimReleaseTimeout bounds the release call, which runs on a context detached
// from the caller's cancellation so a canceled request still records its state.
const claimReleaseTimeout = 10 * time.Second

// purchaseConfig is what a PurchaseOption sets. The zero claimer means the
// in-process lock is the only guard.
type purchaseConfig struct {
	claimer common.PurchaseClaimer
	ttl     time.Duration
	settle  time.Duration
}

// PurchaseOption configures DoIdempotentPurchaseTwoStep.
type PurchaseOption func(*purchaseConfig)

// WithClaimer makes the purchase exclusive per idempotency token across
// processes through c (go#745). A nil c leaves the in-process lock alone.
func WithClaimer(c common.PurchaseClaimer) PurchaseOption {
	return func(cfg *purchaseConfig) { cfg.claimer = c }
}

func newPurchaseConfig(opts []PurchaseOption) *purchaseConfig {
	cfg := &purchaseConfig{
		ttl:    common.DefaultClaimTTL,
		settle: common.DefaultClaimSettleWindow,
	}
	for _, o := range opts {
		o(cfg)
	}
	return cfg
}

// newHolderID is a per-process ID plus a per-call nonce, so two goroutines of
// one process are distinct holders.
func newHolderID() string {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(fmt.Sprintf("purchase claim holder nonce: %v", err))
	}
	return fmt.Sprintf("pid%d-%s", os.Getpid(), hex.EncodeToString(nonce[:]))
}

// claimGuard is the holder side of one acquired claim. A nil guard
// (no claimer configured) is valid for every method.
type claimGuard struct {
	claimer common.PurchaseClaimer
	claim   common.Claim
	ttl     time.Duration
	sending bool
}

// acquireClaim takes the durable claim for token. It returns a nil guard when no
// claimer is configured (logged once per purchase) or when the claim row is
// already completed; in that case the returned Claim carries the recorded order.
// A claim error never falls through to buying.
func acquireClaim(ctx context.Context, cfg *purchaseConfig, token string) (*claimGuard, common.Claim, error) {
	if cfg.claimer == nil {
		log.Printf("no durable purchase claimer for idempotency token %s; relying on the in-process lock only, so two processes can still double-buy (go#745)", common.MaskToken(token))
		return nil, common.Claim{}, nil
	}
	claim, err := cfg.claimer.AcquireClaim(ctx, token, newHolderID(), cfg.ttl, cfg.settle)
	switch {
	case errors.Is(err, common.ErrClaimSettlePending):
		// Already wraps ErrOutcomeUnknown: the earlier holder may still commit.
		return nil, claim, fmt.Errorf("purchase claim for token %s: %w", common.MaskToken(token), err)
	case errors.Is(err, common.ErrClaimHeld):
		return nil, claim, fmt.Errorf("another purchase holds the claim for token %s, not purchasing (a re-drive will adopt its order): %w: %w", common.MaskToken(token), common.ErrRefusedBeforePurchase, err)
	case err != nil:
		return nil, claim, fmt.Errorf("purchase claim for token %s failed (refusing to purchase to avoid a possible double-buy): %w: %w", common.MaskToken(token), common.ErrRefusedBeforePurchase, err)
	}
	if claim.Phase == common.ClaimCompleted {
		return nil, claim, nil
	}
	g := &claimGuard{claimer: cfg.claimer, claim: claim, ttl: cfg.ttl, sending: claim.Phase == common.ClaimSending}
	return g, claim, nil
}

// claimStopErr marks a claim failure for the attempt it stopped. On attempt 1
// no request was sent, so it is a refusal. On attempt 2 or later an earlier
// attempt's purchase may have committed (types.go: a wrapper must not carry
// ErrRefusedBeforePurchase then), so it reads ErrOutcomeUnknown.
func claimStopErr(attempt int, err error) error {
	if attempt <= 1 {
		return fmt.Errorf("%w: %w", common.ErrRefusedBeforePurchase, err)
	}
	return afterUncertainAttempt(attempt, err)
}

// beforeSend advances the claim to sending, synchronously and failing closed,
// the last step before the purchase request.
func (g *claimGuard) beforeSend(ctx context.Context, attempt int) error {
	if g == nil {
		return nil
	}
	if !g.sending {
		if err := g.claimer.AdvanceClaim(ctx, g.claim, common.ClaimSending); err != nil {
			return claimStopErr(attempt, fmt.Errorf("advance purchase claim to sending: %w", err))
		}
		g.sending = true
	}
	return nil
}

// release ends the claim: completed when orderID is
// set, otherwise the store decides by phase (it deletes only a claimed row). A
// failure is logged and left to lease expiry; the caller's result stands.
func (g *claimGuard) release(ctx context.Context, orderID string) {
	if g == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimReleaseTimeout)
	defer cancel()
	if err := g.claimer.ReleaseClaim(rctx, g.claim, common.ClaimOutcome{OrderID: orderID}); err != nil {
		log.Printf("purchase claim release for token %s failed, leaving it to expire: %v", common.MaskToken(g.claim.Token), err)
	}
}
