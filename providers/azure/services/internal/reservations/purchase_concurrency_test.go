package reservations

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOrderAzure is a stateful stand-in for the reservation-order API: the list
// call returns every order purchased so far, calculatePrice mints a new order
// ID and the purchase call records it. The first list call sleeps for listHold
// so a concurrent caller has time to list before any purchase is visible.
type fakeOrderAzure struct {
	token    string
	listHold time.Duration

	mu        sync.Mutex
	listCalls int
	minted    int
	purchased []string
}

func (f *fakeOrderAzure) Do(req *http.Request) (*http.Response, error) {
	switch {
	case req.Method == http.MethodGet && req.URL.String() == listURL:
		return f.list(), nil
	case req.Method == http.MethodPost && req.URL.String() == calcURL:
		return f.calculate(), nil
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/purchase"):
		f.mu.Lock()
		defer f.mu.Unlock()
		f.purchased = append(f.purchased, strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/providers/Microsoft.Capacity/reservationOrders/"), "/purchase"))
		return fakeResp(http.StatusOK, `{}`), nil
	}
	return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
}

func (f *fakeOrderAzure) list() *http.Response {
	f.mu.Lock()
	f.listCalls++
	first := f.listCalls == 1
	body := f.orderListBody()
	f.mu.Unlock()
	if first {
		// The snapshot above is taken before the hold, so this caller sees
		// the state at list time, as a real lookup would.
		time.Sleep(f.listHold)
	}
	return fakeResp(http.StatusOK, body)
}

// orderListBody renders every purchased order as a list page. Caller holds f.mu.
func (f *fakeOrderAzure) orderListBody() string {
	orders := make([]struct {
		Name              string
		IdempotencyToken  string
		ProvisioningState string
		OtherTags         map[string]string
	}, 0, len(f.purchased))
	for _, id := range f.purchased {
		orders = append(orders, struct {
			Name              string
			IdempotencyToken  string
			ProvisioningState string
			OtherTags         map[string]string
		}{Name: id, IdempotencyToken: f.token, ProvisioningState: "Succeeded"})
	}
	return orderListJSON(orders, "")
}

func (f *fakeOrderAzure) calculate() *http.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted++
	return fakeResp(http.StatusOK, fmt.Sprintf(`{"properties":{"billingCurrencyTotal":{"currencyCode":"USD"},"reservationOrderId":"order-%d"}}`, f.minted))
}

// TestDoIdempotentPurchaseTwoStep_ConcurrentSameToken_PurchasesOnce pins issue
// #745: two concurrent purchases with the same idempotency token must not both
// run the list-then-purchase sequence. Before the fix both lists ran before
// either purchase was visible and both bought; the second caller must instead
// adopt the order the first one created.
func TestDoIdempotentPurchaseTwoStep_ConcurrentSameToken_PurchasesOnce(t *testing.T) {
	t.Parallel()
	const token = "concurrent-tok"
	fake := &fakeOrderAzure{token: token, listHold: 200 * time.Millisecond}
	ctx := context.Background()

	var wg sync.WaitGroup
	orderIDs := make([]string, 2)
	existing := make([]bool, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			orderIDs[i], existing[i], errs[i] = DoIdempotentPurchaseTwoStep(ctx, fake, calcURL, []byte(testBody), "tok", token, "")
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Len(t, fake.purchased, 1, "exactly one reservation order may be bought per idempotency token")
	assert.Equal(t, orderIDs[0], orderIDs[1], "both callers must resolve to the same order")
	assert.NotEqual(t, existing[0], existing[1], "one caller bought the order, the other adopted it")
	tokenLocksMu.Lock()
	assert.NotContains(t, tokenLocks, token, "the token's lock entry must be dropped once no caller holds or waits on it")
	tokenLocksMu.Unlock()
}
