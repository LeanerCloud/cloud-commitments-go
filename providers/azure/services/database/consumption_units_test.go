package database

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
		mocks.ReservationRow("SQL Database", "SQL Database Single/Elastic Pool General Purpose - Provisioned - Compute Gen5", "eastus", "GP_Gen5_2", "2 vCore", "vCore", "1 Year", 1500),
		mocks.ReservationRow("SQL Database", "SQL Database Single/Elastic Pool General Purpose - Provisioned - Compute Gen5", "eastus", "GP_Gen5_2", "2 vCore", "vCore", "3 Years", 3200),
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
	mocks.AssertConsumptionUnitVariants(t, recs, 1500, 3200)
}
