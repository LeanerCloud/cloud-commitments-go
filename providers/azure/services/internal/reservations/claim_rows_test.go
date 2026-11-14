package reservations

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// releaseRecorder keeps the error ReleaseClaim returned.
type releaseRecorder struct {
	common.PurchaseClaimer
	mu         sync.Mutex
	releaseErr error
	released   bool
}

func (r *releaseRecorder) ReleaseClaim(ctx context.Context, cl common.Claim, o common.ClaimOutcome) error {
	err := r.PurchaseClaimer.ReleaseClaim(ctx, cl, o)
	r.mu.Lock()
	r.releaseErr, r.released = err, true
	r.mu.Unlock()
	return err
}

// aliasClaimer maps every token to one shared store key, which models two
// processes that hold different in-process locks (the lock is keyed by the
// caller's token string) but contend for the same durable claim row.
type aliasClaimer struct {
	common.PurchaseClaimer
	key string
}

func (a aliasClaimer) AcquireClaim(ctx context.Context, _, holder string, ttl, settle time.Duration) (common.Claim, error) {
	return a.PurchaseClaimer.AcquireClaim(ctx, a.key, holder, ttl, settle)
}

// F5a: a checkpoint that finds the lease gone on attempt 1 refuses before any
// purchase request. The heartbeat never renews (interval of an hour), so the
// local deadline is what the checkpoint sees.
func TestClaim_CheckpointOnAttemptOneRefusesBeforeThePurchase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		ttl       time.Duration
		onCalc    func()
		wantCalcs int
		wantPhase common.ClaimPhase
	}{
		// Lapsed before the first checkpoint: nothing is priced, the claimed row is deleted.
		{"before calculatePrice", time.Nanosecond, nil, 0, ""},
		// Lapsed while pricing: the claim was already advanced to sending, so the
		// store keeps the row and the settle window still guards a later buyer.
		{"between calculatePrice and the purchase", 50 * time.Millisecond, func() { time.Sleep(120 * time.Millisecond) }, 1, common.ClaimSending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newClaimFixture()
			az := &claimAzure{onCalc: tc.onCalc}
			id, existing, err := buy(az, f.store, withClaimTiming(tc.ttl, time.Hour, time.Minute))
			require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
			assert.ErrorIs(t, err, common.ErrClaimLost)
			assert.NotErrorIs(t, err, common.ErrOutcomeUnknown)
			assert.Empty(t, id)
			assert.False(t, existing)
			assert.Zero(t, az.purchases, "no POST was sent")
			assert.Equal(t, tc.wantCalcs, az.calcs)
			row := f.store.Row(claimTok)
			assert.Equal(t, tc.wantPhase, row.Phase)
			assert.Equal(t, tc.wantPhase != "", row.Exists)
		})
	}
}

// F5c: the store expires the lease while the POST is in flight. The POST
// completes and the caller still gets the order ID. Within the settle window a
// peer cannot acquire, so it cannot buy.
func TestClaim_LeaseExpiringDuringThePurchaseStillReturnsTheOrder(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	rec := &releaseRecorder{PurchaseClaimer: f.store}
	var peerErr error
	az := &claimAzure{}
	az.onPurchase = func(int) {
		f.now = f.now.Add(2 * time.Minute) // past the 1m lease, inside the 10m settle window
		_, peerErr = f.store.AcquireClaim(context.Background(), claimTok, "peer", time.Minute, 10*time.Minute)
	}
	id, existing, err := buy(az, rec, withClaimTiming(time.Minute, time.Hour, 10*time.Minute))
	require.NoError(t, err)
	assert.False(t, existing)
	assert.Equal(t, "order-1", id)
	assert.Equal(t, 1, az.purchases)
	require.ErrorIs(t, peerErr, common.ErrClaimSettlePending, "a sending row blocks the peer until the settle window ends")
	assert.ErrorIs(t, peerErr, common.ErrOutcomeUnknown)
	assert.True(t, rec.released)
	require.NoError(t, rec.releaseErr)
	assert.Equal(t, common.ClaimCompleted, f.store.Row(claimTok).Phase)
}

// F5c, lost variant: a peer took the claim over (past the settle window) during
// the POST. The holder still returns its order ID, its Release reports the lost
// claim, and the peer's row is left untouched.
func TestClaim_ClaimTakenOverDuringThePurchaseReportsLostOnRelease(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	rec := &releaseRecorder{PurchaseClaimer: f.store}
	az := &claimAzure{}
	az.onPurchase = func(int) {
		f.now = f.now.Add(20 * time.Minute)
		_, err := f.store.AcquireClaim(context.Background(), claimTok, "peer", time.Minute, 10*time.Minute)
		if err != nil {
			panic(err)
		}
	}
	id, _, err := buy(az, rec, withClaimTiming(time.Minute, time.Hour, 10*time.Minute))
	require.NoError(t, err, "the caller's result stands")
	assert.Equal(t, "order-1", id)
	require.True(t, rec.released)
	assert.ErrorIs(t, rec.releaseErr, common.ErrClaimLost)
	row := f.store.Row(claimTok)
	assert.Equal(t, "peer", row.Holder)
	assert.EqualValues(t, 2, row.Generation)
	assert.Equal(t, common.ClaimSending, row.Phase, "the peer's row is not completed by the late holder")
}

// F6: the cross-process proof. The in-process lock is keyed by the caller's
// token string, so flows with different token strings share no lock; the alias
// store maps them all to one claim row. Exactly one POST may be sent: a second
// flow during the first's POST is refused, and one after it completes adopts.
func TestClaim_TwoFlowsWithoutASharedLockBuyOnce(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	store := aliasClaimer{PurchaseClaimer: f.store, key: claimTok}
	started, release := make(chan struct{}), make(chan struct{})
	az := &claimAzure{byID: map[string]string{"order-1": "Succeeded"}}
	az.onPurchase = func(call int) {
		if call == 1 {
			close(started)
			<-release
		}
	}
	var relOnce sync.Once
	t.Cleanup(func() { relOnce.Do(func() { close(release) }) })

	type result struct {
		id       string
		existing bool
		err      error
	}
	first := make(chan result, 1)
	go func() {
		var r result
		r.id, r.existing, r.err = DoIdempotentPurchaseTwoStep(context.Background(), az, calcURL, []byte(testBody), "bearer", "proc-a", "", WithClaimer(store))
		first <- r
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("first flow never reached the purchase request")
	}

	_, _, err := DoIdempotentPurchaseTwoStep(context.Background(), az, calcURL, []byte(testBody), "bearer", "proc-b", "", WithClaimer(store))
	require.ErrorIs(t, err, common.ErrClaimHeld, "the second flow is blocked by the durable claim, not by an in-process lock")
	assert.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	az.mu.Lock()
	assert.Equal(t, 1, az.purchases)
	assert.Equal(t, 1, az.calcs, "the refused flow priced nothing")
	assert.Equal(t, 1, az.lists, "the refused flow listed nothing")
	az.mu.Unlock()

	relOnce.Do(func() { close(release) })
	r := <-first
	require.NoError(t, r.err)
	assert.False(t, r.existing)
	assert.Equal(t, "order-1", r.id)

	id, existing, err := DoIdempotentPurchaseTwoStep(context.Background(), az, calcURL, []byte(testBody), "bearer", "proc-c", "", WithClaimer(store))
	require.NoError(t, err)
	assert.True(t, existing, "the completed row is adopted")
	assert.Equal(t, "order-1", id)
	assert.Equal(t, 1, az.purchases, "exactly one POST across all flows")
	assert.Equal(t, 1, az.calcs)
}

// F5a: the heartbeat renews, so a slow calculatePrice that outlasts the lease
// does not stop the purchase. Without renewals the local deadline would pass.
func TestClaim_HeartbeatKeepsTheClaimAliveAcrossASlowStep(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	az := &claimAzure{onCalc: func() { time.Sleep(300 * time.Millisecond) }}
	id, _, err := buy(az, f.store, withClaimTiming(100*time.Millisecond, 10*time.Millisecond, time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "order-1", id)
	assert.Equal(t, 1, az.purchases)
}
