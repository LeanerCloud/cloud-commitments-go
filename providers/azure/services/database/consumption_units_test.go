package database

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
// commitment of Count x the term-total reservation retail price (live-shaped
// per-vCore rows x 2 vCores, eastus), for a 1y legacy and a 3y modern recommendation.
func TestGetRecommendations_ConsumptionUnits(t *testing.T) {
	t.Parallel()
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Items: []map[string]any{
		mocks.ReservationRow("SQL Database", "SQL Database Single/Elastic Pool General Purpose - Compute Gen5", "eastus", "SQLDB_GP_Compute_Gen5", "vCore", "vCore", "1 Year", 867),
		mocks.ReservationRow("SQL Database", "SQL Database Single/Elastic Pool General Purpose - Compute Gen5", "eastus", "SQLDB_GP_Compute_Gen5", "vCore", "vCore", "3 Years", 1800),
	}})
	client.SetCapabilitiesClient(&MockCapabilitiesClient{})
	client.SetManagedInstancesPager(&MockSQLManagedInstancesPager{})
	client.SetServersPager(&MockSQLServersPager{})
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("GP_Gen5_2", "eastus"),
		HasMore: true,
	})

	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	mocks.AssertConsumptionUnitVariants(t, recs, 1734, 3600)
}

// A retail price outage must fail the collection, not return a partial list.
func TestGetRecommendations_ConsumptionPriceFetchFailureFailsCollection(t *testing.T) {
	t.Parallel()
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Err: errors.New("connection reset")})
	client.SetCapabilitiesClient(&MockCapabilitiesClient{})
	client.SetManagedInstancesPager(&MockSQLManagedInstancesPager{})
	client.SetServersPager(&MockSQLServersPager{})
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("GP_Gen5_2", "eastus"),
		HasMore: true,
	})

	_, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.Error(t, err)
	assert.ErrorIs(t, err, pricing.ErrFetch)
}
