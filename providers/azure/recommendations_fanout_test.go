package azure

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
)

// Advisor pricing runs after the fan-out finishes, so it must see the caller's
// live ctx. A fan-out helper's own ctx is canceled once the helper returns.
func TestGetRecommendations_AdvisorPricingSeesLiveContextAfterFanOut(t *testing.T) {
	var priceCtxErr error
	probe := &funcPriceFake{price: func(ctx context.Context, _, _ string, _ int) (azrecs.ReservationPrice, error) {
		priceCtxErr = ctx.Err()
		return usd(100), nil
	}}

	recs, err := advisorOnlyAdapter(t, probe, riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D2s_v3", 1, "")).
		GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err)
	require.NotEmpty(t, recs)
	assert.NoError(t, priceCtxErr, "advisor pricing must not run on the fan-out's canceled ctx")
}

// A service the params filter skips was not attempted, so the all-attempted-
// failed guard must not count it: one failing included service plus skipped
// ones is a total failure, not a success with zero rows.
func TestGetRecommendations_SkippedServicesAreNotAttempted(t *testing.T) {
	failing := &fakeServiceClient{err: assert.AnError}
	overrideServiceClientFns(t, failing, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
	a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub", getAdvisorRecsFn: noopAdvisorFn}

	recs, err := a.GetRecommendations(context.Background(), &common.RecommendationParams{Service: common.ServiceCompute})

	assert.Nil(t, recs)
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}
