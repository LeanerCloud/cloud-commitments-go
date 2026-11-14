package reservations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

const claimTok = "claim-tok"

type claimOrder struct{ name, state string }

// claimAzure is a stateful stand-in for the order API with per-call status
// injection: a successful purchase adds an order tagged with the token.
type claimAzure struct {
	mu          sync.Mutex
	orders      []claimOrder // visible to list; the token tags them
	byID        map[string]string
	calcStatus  int
	purchStatus []int // status per purchase call; missing means 200
	purchErr    error
	onCalc      func()
	onPurchase  func(call int)
	calcs       int
	purchases   int
	lists       int
	gets        int
}

func (f *claimAzure) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calcs + f.purchases + f.lists + f.gets
}

func (f *claimAzure) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	switch {
	case req.Method == http.MethodGet && req.URL.String() == listURL:
		f.lists++
		orders := make([]struct {
			Name              string
			IdempotencyToken  string
			ProvisioningState string
			OtherTags         map[string]string
		}, 0, len(f.orders))
		for _, o := range f.orders {
			orders = append(orders, struct {
				Name              string
				IdempotencyToken  string
				ProvisioningState string
				OtherTags         map[string]string
			}{Name: o.name, IdempotencyToken: claimTok, ProvisioningState: o.state})
		}
		body := orderListJSON(orders, "")
		f.mu.Unlock()
		return fakeResp(200, body), nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/reservationOrders/"):
		f.gets++
		id := strings.TrimPrefix(req.URL.Path, "/providers/Microsoft.Capacity/reservationOrders/")
		state, ok := f.byID[id]
		f.mu.Unlock()
		if !ok {
			return fakeResp(404, `{}`), nil
		}
		return fakeResp(200, `{"properties":{"provisioningState":"`+state+`"}}`), nil
	case req.Method == http.MethodPost && req.URL.String() == calcURL:
		f.calcs++
		n, status, hook := f.calcs, f.calcStatus, f.onCalc
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		if status != 0 {
			return fakeResp(status, `{"error":"calc"}`), nil
		}
		return fakeResp(200, fmt.Sprintf(`{"properties":{"billingCurrencyTotal":{"currencyCode":"USD"},"reservationOrderId":"order-%d"}}`, n)), nil
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/purchase"):
		f.purchases++
		call := f.purchases
		status, err, hook := 200, f.purchErr, f.onPurchase
		if call <= len(f.purchStatus) {
			status = f.purchStatus[call-1]
		}
		id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/providers/Microsoft.Capacity/reservationOrders/"), "/purchase")
		if status == 200 && err == nil {
			f.orders = append(f.orders, claimOrder{id, "Succeeded"})
		}
		f.mu.Unlock()
		if hook != nil {
			hook(call)
		}
		if err != nil {
			return nil, err
		}
		if status == 400 {
			return fakeResp(status, `Session timed out`), nil
		}
		return fakeResp(status, `{"error":"purchase"}`), nil
	}
	f.mu.Unlock()
	return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
}

// claimFixture is a memory store with a movable clock the test also uses to age rows.
type claimFixture struct {
	store *common.MemoryPurchaseClaimer
	now   time.Time
}

func newClaimFixture() *claimFixture {
	f := &claimFixture{now: time.Unix(2_000_000, 0)}
	f.store = common.NewMemoryPurchaseClaimer(func() time.Time { return f.now })
	return f
}

// seed leaves the token's row in the given phase, held by a dead holder whose
// lease has expired. settled additionally moves the clock past the settle window.
func (f *claimFixture) seed(t *testing.T, phase common.ClaimPhase, orderID string, settled bool) {
	t.Helper()
	ctx := context.Background()
	c, err := f.store.AcquireClaim(ctx, claimTok, "dead", time.Minute, 10*time.Minute)
	require.NoError(t, err)
	switch phase {
	case common.ClaimSending:
		require.NoError(t, f.store.AdvanceClaim(ctx, c, common.ClaimSending))
	case common.ClaimCompleted:
		require.NoError(t, f.store.ReleaseClaim(ctx, c, common.ClaimOutcome{OrderID: orderID}))
	}
	f.now = f.now.Add(2 * time.Minute)
	if settled {
		f.now = f.now.Add(10 * time.Minute)
	}
}

func buy(f *claimFixture, az HTTPClient, claimer common.PurchaseClaimer, opts ...PurchaseOption) (string, bool, error) {
	return DoIdempotentPurchaseTwoStep(context.Background(), az, calcURL, []byte(testBody), "bearer", claimTok, "",
		append([]PurchaseOption{WithClaimer(claimer)}, opts...)...)
}

func TestClaim_SuccessCompletesTheRow(t *testing.T) {
	t.Parallel()
	f, az := newClaimFixture(), &claimAzure{}
	id, existing, err := buy(f, az, f.store)
	require.NoError(t, err)
	assert.False(t, existing)
	assert.Equal(t, 1, az.purchases)
	row := f.store.Row(claimTok)
	assert.Equal(t, common.ClaimCompleted, row.Phase)
	assert.Equal(t, id, row.OrderID)
}

func TestClaim_NoClaimerKeepsTheInProcessBehaviour(t *testing.T) {
	t.Parallel()
	az := &claimAzure{}
	_, _, err := DoIdempotentPurchaseTwoStep(context.Background(), az, calcURL, []byte(testBody), "bearer", claimTok, "")
	require.NoError(t, err)
	assert.Equal(t, 1, az.purchases)
}

func TestClaim_LoserSettleAndStoreErrorsBuyNothing(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	held, err := f.store.AcquireClaim(context.Background(), claimTok, "winner", time.Minute, time.Minute)
	require.NoError(t, err)
	_ = held
	az := &claimAzure{}
	_, _, err = buy(f, az, f.store)
	require.ErrorIs(t, err, common.ErrClaimHeld)
	assert.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Zero(t, az.calls(), "a loser makes no Azure call")

	g := newClaimFixture()
	g.seed(t, common.ClaimSending, "", false)
	_, _, err = buy(g, az, g.store)
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.NotErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Zero(t, az.calls())

	_, _, err = buy(f, az, &failingClaimer{acquireErr: errors.New("store down")})
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Zero(t, az.calls(), "a claim error fails closed")
}

// failingClaimer wraps a store with injectable failures and records release state.
type failingClaimer struct {
	common.PurchaseClaimer
	acquireErr, advanceErr error
}

func (c *failingClaimer) AcquireClaim(ctx context.Context, token, holder string, ttl, settle time.Duration) (common.Claim, error) {
	if c.acquireErr != nil {
		return common.Claim{}, c.acquireErr
	}
	return c.PurchaseClaimer.AcquireClaim(ctx, token, holder, ttl, settle)
}

func (c *failingClaimer) AdvanceClaim(ctx context.Context, cl common.Claim, p common.ClaimPhase) error {
	if c.advanceErr != nil {
		return c.advanceErr
	}
	return c.PurchaseClaimer.AdvanceClaim(ctx, cl, p)
}

// F9: the release decision follows the phase, never the error class.
func TestClaim_ReleaseByPhase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		az        *claimAzure
		wantPhase common.ClaimPhase // "" means the row is deleted
	}{
		{"purchase 5xx", &claimAzure{purchStatus: []int{500}}, common.ClaimSending},
		{"purchase 409", &claimAzure{purchStatus: []int{409}}, common.ClaimSending},
		{"purchase transport error", &claimAzure{purchErr: errors.New("connection reset")}, common.ClaimSending},
		{"calculatePrice failure", &claimAzure{calcStatus: 500}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newClaimFixture()
			_, _, err := buy(f, tc.az, f.store)
			require.Error(t, err)
			row := f.store.Row(claimTok)
			assert.Equal(t, tc.wantPhase, row.Phase)
			assert.Equal(t, tc.wantPhase != "", row.Exists)
		})
	}
}

func TestClaim_AdvanceErrorSendsNoPurchase(t *testing.T) {
	t.Parallel()
	f, az := newClaimFixture(), &claimAzure{}
	_, _, err := buy(f, az, &failingClaimer{PurchaseClaimer: f.store, advanceErr: errors.New("store down")})
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Zero(t, az.purchases)
	assert.False(t, f.store.Row(claimTok).Exists, "still claimed, so the row is deleted")
}

// F9b: a holder that took over a sending row never deletes it, even when its own
// attempt sent nothing, because the earlier holder's POST may have committed.
func TestClaim_TakeoverOfSendingRowIsNeverDeleted(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	f.seed(t, common.ClaimSending, "", true)
	az := &claimAzure{calcStatus: 500}
	_, _, err := buy(f, az, f.store)
	require.Error(t, err)
	assert.Equal(t, 1, az.lists, "the list is checked before buying")
	row := f.store.Row(claimTok)
	require.True(t, row.Exists)
	assert.Equal(t, common.ClaimSending, row.Phase)
	assert.EqualValues(t, 2, row.Generation)
}

func TestClaim_TakeoverOfSendingRowAdoptsAnOrderTheListShows(t *testing.T) {
	t.Parallel()
	f := newClaimFixture()
	f.seed(t, common.ClaimSending, "", true)
	az := &claimAzure{orders: []claimOrder{{"order-late", "Succeeded"}}}
	id, existing, err := buy(f, az, f.store)
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, "order-late", id)
	assert.Zero(t, az.purchases)
	assert.Equal(t, common.ClaimCompleted, f.store.Row(claimTok).Phase)
}

// F6b / F6c: a completed row wins; its order state decides adopt or fail loud.
func TestClaim_CompletedRowByRecordedOrderState(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"Failed", "Cancelled", "Expired", "BillingFailed", "FutureState"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newClaimFixture()
			f.seed(t, common.ClaimCompleted, "order-9", false)
			az := &claimAzure{byID: map[string]string{"order-9": state}}
			id, existing, err := buy(f, az, f.store)
			require.Error(t, err)
			assert.Empty(t, id)
			assert.False(t, existing)
			assert.ErrorContains(t, err, "order-9")
			blocked := state == "BillingFailed" || state == "FutureState"
			assert.Equal(t, blocked, errors.Is(err, ErrReservationOrderBlocked))
			assert.Zero(t, az.purchases+az.calcs+az.lists, "no purchase and no list: only the GET by ID")
			assert.Equal(t, 1, az.gets)
		})
	}
	f := newClaimFixture()
	f.seed(t, common.ClaimCompleted, "order-9", false)
	az := &claimAzure{byID: map[string]string{"order-9": "Succeeded"}}
	id, existing, err := buy(f, az, f.store)
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, "order-9", id)
	assert.Zero(t, az.purchases+az.calcs+az.lists)
}
