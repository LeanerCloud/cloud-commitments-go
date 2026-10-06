package synapse

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// TestGetRecommendations_ConsumptionUnits pins the units of the Consumption
// path: monthly on-demand and savings from the lookback-window amounts, and a
// commitment of Count x the term-total reservation retail price (synthetic
// rows, not live quotes), for a 1y legacy and a 3y modern recommendation.
func TestGetRecommendations_ConsumptionUnits(t *testing.T) {
	client := newPricedTestClient()
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("DW1000c", "eastus"),
		HasMore: true,
	})

	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	mocks.AssertConsumptionUnitVariants(t, recs, 1000, 2400)
}

// A retail price outage must fail the collection, not return a partial list.
func TestGetRecommendations_ConsumptionPriceFetchFailureFailsCollection(t *testing.T) {
	client := newTestClient()
	client.httpClient = &mocks.PricingHTTP{Err: errors.New("connection reset")}
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("DW1000c", "eastus"),
		HasMore: true,
	})

	_, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.Error(t, err)
	assert.ErrorIs(t, err, pricing.ErrFetch)
}
