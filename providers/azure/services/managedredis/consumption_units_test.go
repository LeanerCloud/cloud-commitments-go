package managedredis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// TestGetRecommendations_ConsumptionUnits pins the units of the Consumption
// path: monthly on-demand and savings from the lookback-window amounts, and a
// commitment of Count x the term-total reservation retail price (synthetic
// rows, not live quotes), for a 1y legacy and a 3y modern recommendation.
func TestGetRecommendations_ConsumptionUnits(t *testing.T) {
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Items: []map[string]any{
		mocks.ReservationRow("Redis Cache", "Azure Redis Cache Premium", "eastus", "Azure_Redis_Cache_Premium_P1_Cache", "P1", "P1 Cache Instance", "1 Year", 1100),
		mocks.ReservationRow("Redis Cache", "Azure Redis Cache Premium", "eastus", "Azure_Redis_Cache_Premium_P1_Cache", "P1", "P1 Cache Instance", "3 Years", 2600),
	}})
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("Premium_P1", "eastus"),
		HasMore: true,
	})

	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	mocks.AssertConsumptionUnitVariants(t, recs, 1100, 2600)
}

func TestRecommendationsListArgs_SetsLookBackExplicitly(t *testing.T) {
	_, opts := NewClient(nil, "sub", "eastus").recommendationsListArgs()
	require.NotNil(t, opts.Filter)
	require.Equal(t, "properties/scope eq 'Shared' and properties/resourceType eq 'RedisCache' and properties/lookBackPeriod eq 'Last7Days'", *opts.Filter)
}
