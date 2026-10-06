package compute

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
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
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Items: []map[string]any{
		mocks.ReservationRow("Virtual Machines", "Virtual Machines Das v4 Series", "eastus", "Standard_D2as_v4", "D2as v4", "D2as v4", "1 Year", 494),
		mocks.ReservationRow("Virtual Machines", "Virtual Machines Das v4 Series", "eastus", "Standard_D2as_v4", "D2as v4", "D2as v4", "3 Years", 949),
	}})
	client.SetResourceSKUsPager(&mocks.MockResourceSKUsPager{})
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{
		Results: mocks.ConsumptionUnitFixtures("Standard_D2as_v4", "eastus"),
		HasMore: true,
	})

	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	mocks.AssertConsumptionUnitVariants(t, recs, 494, 949)
}

// TestGetRecommendations_ConsumptionSkipsAndDedupes covers the per-recommendation
// failure modes (each skipped while the others still return) and that one
// retail lookup serves every recommendation sharing (sku, region, term).
func TestGetRecommendations_ConsumptionSkipsAndDedupes(t *testing.T) {
	http := &mocks.PricingHTTP{Items: []map[string]any{
		mocks.ReservationRow("Virtual Machines", "Virtual Machines Das v4 Series", "eastus", "Standard_D2as_v4", "D2as v4", "D2as v4", "1 Year", 494),
	}}
	client := NewClientWithHTTP(nil, "sub", "eastus", http)
	client.SetResourceSKUsPager(&mocks.MockResourceSKUsPager{})

	ok := func(region string) *armconsumption.LegacyReservationRecommendation {
		return mocks.BuildLegacyReservationRecommendation(mocks.WithRegion(region), mocks.WithSKU("Standard_D2as_v4"),
			mocks.WithQuantity(12), mocks.WithCosts(191, 116, 75))
	}
	recs := []armconsumption.ReservationRecommendationClassification{
		ok("eastus"),
		ok("eastus"), // same key: no second lookup
		mocks.BuildLegacyReservationRecommendation(mocks.WithSKU("Standard_NoPrice"), mocks.WithCosts(191, 116, 75)),
		mocks.BuildLegacyReservationRecommendation(mocks.WithSKU("Standard_D2as_v4"), mocks.WithCosts(191, 116, 75), mocks.WithLookBack("")),
		mocks.BuildLegacyReservationRecommendation(mocks.WithSKU("Standard_D2as_v4"), mocks.WithCosts(191, 116, 75), mocks.WithTerm("P2Y")),
		mocks.BuildModernReservationRecommendation(mocks.WithModernSKUName("Standard_D2as_v4"), mocks.WithModernCosts(191, 116, 75), mocks.WithModernCurrency("EUR")),
	}
	client.SetRecommendationsPager(&mocks.MockRecommendationsPager{Results: recs, HasMore: true})

	got, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err, "a skipped recommendation must not fail the collection")
	require.Len(t, got, 4, "only the two priceable recommendations expand")
	assert.InDelta(t, 12*494.0, got[0].CommitmentCost, 1e-9)
	assert.Equal(t, 2, http.Calls(), "one lookup for the shared key plus one lookup that finds no row for the unpriceable SKU")
}

// A retail API failure must fail the collection, not return a partial list.
func TestGetRecommendations_ConsumptionPriceFetchFailureFailsCollection(t *testing.T) {
	good := mocks.BuildLegacyReservationRecommendation(mocks.WithSKU("Standard_D2as_v4"), mocks.WithQuantity(12), mocks.WithCosts(191, 116, 75))
	for name, http := range map[string]*mocks.PricingHTTP{
		"HTTP 500":  {FailMatch: "Standard_D2as_v4"},
		"transport": {Err: errors.New("connection reset")},
	} {
		t.Run(name, func(t *testing.T) {
			client := NewClientWithHTTP(nil, "sub", "eastus", http)
			client.SetResourceSKUsPager(&mocks.MockResourceSKUsPager{})
			client.SetRecommendationsPager(&mocks.MockRecommendationsPager{Results: []armconsumption.ReservationRecommendationClassification{good}, HasMore: true})

			_, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
			require.Error(t, err)
			assert.ErrorIs(t, err, pricing.ErrFetch)
		})
	}
}
