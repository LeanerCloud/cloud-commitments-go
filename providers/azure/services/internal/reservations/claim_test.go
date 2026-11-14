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
	calcStatus  int
	purchStatus []int // status per purchase call; missing means 200
	purchErr    error
	calcs       int
	purchases   int
	lists       int
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
	case req.Method == http.MethodPost && req.URL.String() == calcURL:
		f.calcs++
		n, status := f.calcs, f.calcStatus
		f.mu.Unlock()
		if status != 0 {
			return fakeResp(status, `{"error":"calc"}`), nil
		}
		return fakeResp(200, fmt.Sprintf(`{"properties":{"billingCurrencyTotal":{"currencyCode":"USD"},"reservationOrderId":"order-%d"}}`, n)), nil
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/purchase"):
		f.purchases++
		call := f.purchases
		status, err := 200, f.purchErr
		if call <= len(f.purchStatus) {
			status = f.purchStatus[call-1]
		}
		id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/providers/Microsoft.Capacity/reservationOrders/"), "/purchase")
		if status == 200 && err == nil {
			f.orders = append(f.orders, claimOrder{id, "Succeeded"})
		}
		f.mu.Unlock()
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

// failingClaimer wraps a store with injectable failures.
type failingClaimer struct {
	common.PurchaseClaimer
	advanceErr error
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
