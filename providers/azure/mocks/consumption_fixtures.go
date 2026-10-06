package mocks

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Window amounts used by ConsumptionUnitFixtures. They are hand-built in the
// shape of the Microsoft docs sample; no recorded Azure response exists.
const (
	fixtureQty         = 2
	fixture1yOnDemand  = 191.0 // legacy, Last7Days
	fixture1yTotal     = 116.0
	fixture3yOnDemand  = 700.0 // modern, 30-day window
	fixture3yTotal     = 400.0
	fixtureDaysPerMon  = 30.4375
	fixtureLegacyWinDy = 7.0
	fixtureModernWinDy = 30.0
)

// ConsumptionUnitFixtures returns one 1-year legacy recommendation (7-day
// window) and one 3-year modern recommendation (30-day window) for sku in
// region, both for 2 units, so a service test can cover both response shapes,
// both terms and two window lengths in a single GetRecommendations call.
func ConsumptionUnitFixtures(sku, region string) []armconsumption.ReservationRecommendationClassification {
	return []armconsumption.ReservationRecommendationClassification{
		BuildLegacyReservationRecommendation(
			WithRegion(region), WithTerm("P1Y"), WithQuantity(fixtureQty), WithSKU(sku),
			WithCosts(fixture1yOnDemand, fixture1yTotal, fixture1yOnDemand-fixture1yTotal), WithLookBack("Last7Days")),
		BuildModernReservationRecommendation(
			WithModernRegion(region), WithModernTerm("P3Y"), WithModernQuantity(fixtureQty), WithModernSKUName(sku),
			WithModernCosts(fixture3yOnDemand, fixture3yTotal, fixture3yOnDemand-fixture3yTotal), WithModernLookBack(&[]int32{30}[0])),
	}
}

// AssertConsumptionUnitVariants checks the four variants produced from
// ConsumptionUnitFixtures given the 1y and 3y per-unit reservation prices:
// monthly on-demand and savings, upfront commitment = Count x price, monthly
// variants with no upfront and price/term-months recurring.
func AssertConsumptionUnitVariants(t *testing.T, recs []common.Recommendation, price1y, price3y float64) {
	t.Helper()
	require.Len(t, recs, 4, "two recommendations expand to four variants")
	cases := []struct {
		upfront, monthly      common.Recommendation
		onDemand, total, days float64
		unit, months          float64
		term                  string
	}{
		{recs[0], recs[1], fixture1yOnDemand, fixture1yTotal, fixtureLegacyWinDy, price1y, 12, "1yr"},
		{recs[2], recs[3], fixture3yOnDemand, fixture3yTotal, fixtureModernWinDy, price3y, 36, "3yr"},
	}
	for i := range cases {
		c := &cases[i]
		assert.Equal(t, "upfront", c.upfront.PaymentOption)
		assert.Equal(t, "monthly", c.monthly.PaymentOption)
		for _, r := range []*common.Recommendation{&c.upfront, &c.monthly} {
			assert.Equal(t, c.term, r.Term)
			assert.InDelta(t, c.onDemand*fixtureDaysPerMon/c.days, r.OnDemandCost, 1e-6)
			assert.InDelta(t, (c.onDemand-c.total)*fixtureDaysPerMon/c.days, r.EstimatedSavings, 1e-6)
		}
		assert.InDelta(t, fixtureQty*c.unit, c.upfront.CommitmentCost, 1e-6)
		require.NotNil(t, c.upfront.RecurringMonthlyCost)
		assert.Zero(t, *c.upfront.RecurringMonthlyCost)
		assert.Zero(t, c.monthly.CommitmentCost)
		require.NotNil(t, c.monthly.RecurringMonthlyCost)
		assert.InDelta(t, fixtureQty*c.unit/c.months, *c.monthly.RecurringMonthlyCost, 1e-6)
	}
	assert.Greater(t, recs[2].CommitmentCost, recs[0].CommitmentCost, "3y upfront exceeds 1y upfront for the same quantity")
}
