package reservations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// claimReleaseTimeout bounds the release call, which runs on a context detached
// from the caller's cancellation so a canceled request still records its state.
const claimReleaseTimeout = 10 * time.Second

// purchaseConfig is what a PurchaseOption sets. The zero claimer means the
// in-process lock is the only guard.
type purchaseConfig struct {
	claimer    common.PurchaseClaimer
	ttl        time.Duration
	renewEvery time.Duration
	settle     time.Duration
}

// PurchaseOption configures DoIdempotentPurchaseTwoStep.
type PurchaseOption func(*purchaseConfig)

// WithClaimer makes the purchase exclusive per idempotency token across
// processes through c (go#745). A nil c leaves the in-process lock alone.
func WithClaimer(c common.PurchaseClaimer) PurchaseOption {
	return func(cfg *purchaseConfig) { cfg.claimer = c }
}

// withClaimTiming overrides the lease numbers; for tests.
func withClaimTiming(ttl, renewEvery, settle time.Duration) PurchaseOption {
	return func(cfg *purchaseConfig) { cfg.ttl, cfg.renewEvery, cfg.settle = ttl, renewEvery, settle }
}

func newPurchaseConfig(opts []PurchaseOption) *purchaseConfig {
	cfg := &purchaseConfig{
		ttl:        common.DefaultClaimTTL,
		renewEvery: common.DefaultClaimRenewInterval,
		settle:     common.DefaultClaimSettleWindow,
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

// claimGuard is the holder side of one acquired claim: it renews the lease in
// the background and answers whether the claim is still valid. A nil guard
// (no claimer configured) is valid for every method.
type claimGuard struct {
	claimer  common.PurchaseClaimer
	lostErr  error
	stop     context.CancelFunc
	done     chan struct{}
	deadline time.Time // local, monotonic; never later than the store's expiry
	claim    common.Claim

	mu      sync.Mutex
	ttl     time.Duration
	sending bool // only the purchase goroutine touches it
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
	start := time.Now()
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
	hbCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	g := &claimGuard{
		claimer: cfg.claimer, claim: claim, ttl: cfg.ttl,
		deadline: start.Add(cfg.ttl), sending: claim.Phase == common.ClaimSending,
		stop: stop, done: make(chan struct{}),
	}
	go g.heartbeat(hbCtx, cfg.renewEvery)
	return g, claim, nil
}

func (g *claimGuard) heartbeat(ctx context.Context, every time.Duration) {
	defer close(g.done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.renewOnce(ctx, every)
		}
	}
}

// renewOnce extends the lease. The local deadline is measured from before the
// request, so it is never later than the store's. A lost claim, or renewals
// failing until the local deadline passes, flags the claim lost for good.
func (g *claimGuard) renewOnce(ctx context.Context, timeout time.Duration) {
	start := time.Now()
	rctx, cancel := context.WithTimeout(ctx, timeout)
	err := g.claimer.RenewClaim(rctx, g.claim, g.ttl)
	cancel()
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.lostErr != nil:
	case err == nil:
		g.deadline = start.Add(g.ttl)
	case errors.Is(err, common.ErrClaimLost):
		g.lostErr = err
	case time.Now().After(g.deadline):
		g.lostErr = fmt.Errorf("%w: renewals kept failing past the local deadline: %w", common.ErrClaimLost, err)
	default:
		log.Printf("purchase claim renewal for token %s failed, will retry: %v", common.MaskToken(g.claim.Token), err)
	}
}

// check reports whether the claim is still valid.
func (g *claimGuard) check() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lostErr == nil && time.Now().After(g.deadline) {
		g.lostErr = fmt.Errorf("%w: lease deadline passed without a renewal", common.ErrClaimLost)
	}
	return g.lostErr
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

// checkpoint aborts the attempt when the claim is no longer valid.
func (g *claimGuard) checkpoint(attempt int) error {
	if err := g.check(); err != nil {
		return claimStopErr(attempt, err)
	}
	return nil
}

// beforeSend advances the claim to sending, synchronously and failing closed,
// then re-checks validity: it is the last step before the purchase request.
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
	return g.checkpoint(attempt)
}

// release stops the heartbeat and ends the claim: completed when orderID is
// set, otherwise the store decides by phase (it deletes only a claimed row). A
// failure is logged and left to lease expiry; the caller's result stands.
func (g *claimGuard) release(ctx context.Context, orderID string) {
	if g == nil {
		return
	}
	g.stop()
	<-g.done
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimReleaseTimeout)
	defer cancel()
	if err := g.claimer.ReleaseClaim(rctx, g.claim, common.ClaimOutcome{OrderID: orderID}); err != nil {
		log.Printf("purchase claim release for token %s failed, leaving it to expire: %v", common.MaskToken(g.claim.Token), err)
	}
}
