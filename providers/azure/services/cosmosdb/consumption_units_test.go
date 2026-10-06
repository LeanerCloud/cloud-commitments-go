package cosmosdb

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
	// Cosmos reservation rows are region "Global", priced per 100 RU/s unit,
	// the same row and unit the purchase path bills (quantity = Count).
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Items: []map[string]any{
		mocks.ReservationRow("Azure Cosmos DB", "Azure Cosmos DB", "Global", "Cosmos_DB_100_RUs", "100 RU/s", "100 RU/s", "1 Year", 60),
		mocks.ReservationRow("Azure Cosmos DB", "Azure Cosmos DB", "Global", "Cosmos_DB_100_RUs", "100 RU/s", "100 RU/s", "3 Years", 140),
	}})
	client.SetCosmosAccountsPager(&MockCosmosAccountsPager{})
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("Cosmos_DB_100_RUs", "eastus"),
		HasMore: true,
	})

	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	mocks.AssertConsumptionUnitVariants(t, recs, 60, 140)
}

// A retail price outage must fail the collection, not return a partial list.
func TestGetRecommendations_ConsumptionPriceFetchFailureFailsCollection(t *testing.T) {
	// Cosmos reservation rows are region "Global", priced per 100 RU/s unit,
	// the same row and unit the purchase path bills (quantity = Count).
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Err: errors.New("connection reset")})
	client.SetCosmosAccountsPager(&MockCosmosAccountsPager{})
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("Cosmos_DB_100_RUs", "eastus"),
		HasMore: true,
	})

	_, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.Error(t, err)
	assert.ErrorIs(t, err, pricing.ErrFetch)
}
