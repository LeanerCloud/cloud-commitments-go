package compute_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
)

// exchangeSDKRequest is one HTTP request the real armreservations clients
// sent through the fake transport.
type exchangeSDKRequest struct {
	method, path string
	body         string
}

// exchangeSDKTransport answers the CalculateExchange and Exchange POSTs with
// a synchronous 200 (no polling headers, so the SDK poller finishes at once)
// and records every request so tests can assert on the exact wire body.
type exchangeSDKTransport struct {
	mu       sync.Mutex
	requests []exchangeSDKRequest
}

func (t *exchangeSDKTransport) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	t.requests = append(t.requests, exchangeSDKRequest{method: req.Method, path: req.URL.Path, body: string(body)})
	t.mu.Unlock()

	resp := `{"status":"Succeeded","properties":{"sessionId":"session-abc"}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(resp)),
		Request:    req,
	}, nil
}

func (t *exchangeSDKTransport) recorded() []exchangeSDKRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]exchangeSDKRequest(nil), t.requests...)
}

// sdkExchangeClient returns a client whose CalculateExchange and
// ExecuteExchange go through the production SDK wrappers and the SDK's own
// JSON serialization, ending at the fake transport instead of Azure.
func sdkExchangeClient(t *testing.T) (*compute.Client, *exchangeSDKTransport) {
	t.Helper()
	transport := &exchangeSDKTransport{}
	client := compute.NewClient(inventorySDKCredential{}, "sub-1", "eastus")
	client.SetExchangeClientOptionsForTest(&arm.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: transport,
		Retry:     policy.RetryOptions{MaxRetries: -1},
	}})
	return client, transport
}

// purchaseBillingPlans decodes a recorded CalculateExchange body and returns
// the raw billingPlan value of each reservationsToPurchase entry.
func purchaseBillingPlans(t *testing.T, body string) []string {
	t.Helper()
	var decoded struct {
		Properties struct {
			ReservationsToPurchase []struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"reservationsToPurchase"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &decoded))
	plans := make([]string, 0, len(decoded.Properties.ReservationsToPurchase))
	for _, p := range decoded.Properties.ReservationsToPurchase {
		var plan string
		require.NoError(t, json.Unmarshal(p.Properties["billingPlan"], &plan))
		plans = append(plans, plan)
	}
	return plans
}

// calculateAndExecute runs the full priced-then-committed flow the platform
// handler runs, and returns the two recorded requests.
func calculateAndExecute(t *testing.T, targets []compute.ExchangeTarget) (calculate, exchange exchangeSDKRequest) {
	t.Helper()
	client, transport := sdkExchangeClient(t)
	preview, _, err := client.CalculateExchange(context.Background(), []compute.ExchangeableReservation{validSource()}, targets)
	require.NoError(t, err)
	_, err = client.ExecuteExchange(context.Background(), preview.SessionID)
	require.NoError(t, err)

	reqs := transport.recorded()
	require.Len(t, reqs, 2)
	assert.Equal(t, http.MethodPost, reqs[0].method)
	assert.Equal(t, "/providers/Microsoft.Capacity/calculateExchange", reqs[0].path)
	assert.Equal(t, http.MethodPost, reqs[1].method)
	assert.Equal(t, "/providers/Microsoft.Capacity/exchange", reqs[1].path)
	return reqs[0], reqs[1]
}

// Issue #89: an unset BillingPlan must keep sending exactly the request
// every exchange sent before the field existed -- Upfront -- so existing
// callers are unaffected.
func TestExchangeSDK_UnsetBillingPlanKeepsUpfrontWireBody(t *testing.T) {
	t.Parallel()
	calculate, exchange := calculateAndExecute(t, []compute.ExchangeTarget{validTarget()})

	const wantCalculate = `{"properties":{"reservationsToExchange":[` +
		`{"quantity":2,"reservationId":"/providers/Microsoft.Capacity/reservationOrders/order-1/reservations/res-1"}],` +
		`"reservationsToPurchase":[{"location":"eastus","properties":{"appliedScopeType":"Shared","billingPlan":"Upfront",` +
		`"billingScopeId":"/subscriptions/sub-1","quantity":1,"renew":false,` +
		`"reservedResourceProperties":{"instanceFlexibility":"On"},"reservedResourceType":"VirtualMachines","term":"P1Y"},` +
		`"sku":{"name":"Standard_D4s_v3"}}]}}`
	assert.JSONEq(t, wantCalculate, calculate.body)
	assert.JSONEq(t, `{"properties":{"sessionId":"session-abc"}}`, exchange.body)
}

// Issue #89: a Monthly target must be priced as Monthly. The exchange
// request carries only the session ID, so the Monthly plan priced in that
// session is the one Azure commits; nothing on the execute leg can turn it
// back into Upfront.
func TestExchangeSDK_MonthlyBillingPlanReachesWire(t *testing.T) {
	t.Parallel()
	monthly := validTarget()
	monthly.BillingPlan = armreservations.ReservationBillingPlanMonthly
	upfront := validTarget()
	upfront.BillingPlan = armreservations.ReservationBillingPlanUpfront

	calculate, exchange := calculateAndExecute(t, []compute.ExchangeTarget{monthly, upfront})

	assert.Equal(t, []string{"Monthly", "Upfront"}, purchaseBillingPlans(t, calculate.body),
		"each target must keep its own billing plan")
	assert.JSONEq(t, `{"properties":{"sessionId":"session-abc"}}`, exchange.body,
		"execute must commit the priced session, not re-send purchase terms")
}

// Issue #89: an unrecognized billing plan is a validation error raised
// before any HTTP request, never a silent Upfront purchase.
func TestExchangeSDK_InvalidBillingPlanRejectedBeforeHTTP(t *testing.T) {
	t.Parallel()
	for _, plan := range []armreservations.ReservationBillingPlan{"monthly", "UPFRONT", "Annual", " Monthly"} {
		t.Run(string(plan), func(t *testing.T) {
			client, transport := sdkExchangeClient(t)
			bad := validTarget()
			bad.BillingPlan = plan

			preview, offerings, err := client.CalculateExchange(context.Background(),
				[]compute.ExchangeableReservation{validSource()},
				[]compute.ExchangeTarget{validTarget(), bad})
			require.Error(t, err)
			assert.Contains(t, err.Error(), `targets[1].billing_plan "`+string(plan)+`" is not a supported billing plan`)
			assert.Nil(t, preview)
			assert.Nil(t, offerings)
			assert.Empty(t, transport.recorded(), "no request may reach Azure for an invalid billing plan")
		})
	}
}
