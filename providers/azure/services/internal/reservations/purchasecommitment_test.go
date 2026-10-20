package reservations

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

var testParams = PurchaseParams{
	ReservedResourceType: "CosmosDb",
	DisplayName:          func(common.Recommendation) string { return "dn" },
}

var testRec = common.Recommendation{ResourceType: "sku1", Count: 3, Term: "1yr", PaymentOption: "all-upfront"}

func TestBuildPurchaseBody_Shape(t *testing.T) {
	b, err := BuildPurchaseBody(testParams, testRec, common.PurchaseOptions{Source: "cudly", IdempotencyToken: "tok"}, "westus", "sub1", armreservations.ReservationBillingPlanUpfront)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"reservedResourceType":"CosmosDb"`)
	assert.Contains(t, string(b), `"billingScopeId":"/subscriptions/sub1"`)
	assert.Contains(t, string(b), `"location":"westus"`)
	assert.Contains(t, string(b), `"term":"P1Y"`)
	assert.Contains(t, string(b), `"quantity":3`)
	assert.Contains(t, string(b), `"billingPlan":"Upfront"`)
	assert.Contains(t, string(b), `"appliedScopeType":"Shared"`)
	assert.Contains(t, string(b), `"tags"`)
}

func TestBuildPurchaseBody_NoSourceNoTokenSkipsTags(t *testing.T) {
	b, err := BuildPurchaseBody(testParams, testRec, common.PurchaseOptions{}, "westus", "sub1", armreservations.ReservationBillingPlanUpfront)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"tags"`)
	assert.Contains(t, string(b), `"displayName":"dn"`)
}

func TestBuildPurchaseBody_BadTermReturnedRaw(t *testing.T) {
	rec := testRec
	rec.Term = "2yr"
	_, err := BuildPurchaseBody(testParams, rec, common.PurchaseOptions{}, "r", "s", armreservations.ReservationBillingPlanUpfront)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported reservation term")
	assert.NotContains(t, err.Error(), "failed to marshal")
}

func TestSubmit_EmptyTokenSkipsLookup(t *testing.T) {
	capt := &reservationstest.Capture{}
	cred := &reservationstest.CountingCred{}
	id, existing, err := Submit(context.Background(), cred, capt, []byte(`{}`), common.PurchaseOptions{})
	require.NoError(t, err)
	assert.Equal(t, "captured-order", id)
	assert.False(t, existing)
	assert.Equal(t, 1, cred.Calls)
	for _, r := range capt.Requests {
		assert.NotEqual(t, http.MethodGet, r.Method, "no idempotency lookup without a token")
	}
}

func TestSubmit_TokenFailureWrapped(t *testing.T) {
	boom := errors.New("boom")
	capt := &reservationstest.Capture{}
	_, _, err := Submit(context.Background(), &reservationstest.CountingCred{Err: boom}, capt, []byte(`{}`), common.PurchaseOptions{})
	require.ErrorIs(t, err, boom)
	assert.EqualError(t, err, "failed to get access token: boom")
	assert.Zero(t, capt.Calls)
}

func TestSubmit_TwoStepErrorUnwrapped(t *testing.T) {
	boom := errors.New("lookup down")
	capt := &reservationstest.Capture{FailWhen: func(*reservationstest.Request) error { return boom }}
	_, _, err := Submit(context.Background(), &reservationstest.CountingCred{}, capt, []byte(`{}`), common.PurchaseOptions{IdempotencyToken: "tok"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to purchase to avoid a possible double-buy")
	assert.NotContains(t, err.Error(), "failed to get access token")
}
