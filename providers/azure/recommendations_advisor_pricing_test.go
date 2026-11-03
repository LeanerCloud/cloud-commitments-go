package azure

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
)

type fakePrices struct {
	calls  int
	prices map[string]azrecs.ReservationPrice
	err    error
}

func (f *fakePrices) lookup(_ context.Context, sku, _ string, _ int) (azrecs.ReservationPrice, error) {
	f.calls++
	if f.err != nil {
		return azrecs.ReservationPrice{}, f.err
	}
	p, ok := f.prices[sku]
	if !ok {
		return azrecs.ReservationPrice{}, fmt.Errorf("no reservation pricing found for %s", sku)
	}
	return p, nil
}

func usd(total float64) azrecs.ReservationPrice {
	return azrecs.ReservationPrice{Total: total, Currency: "USD"}
}

func vmBase(region, sku string, count int) common.Recommendation {
	return riRow(common.ServiceCompute, region, "1yr", sku, count, "")
}

func priceRows(t *testing.T, f *fakePrices, rows ...common.Recommendation) ([]common.Recommendation, *azrecs.SkipLog) {
	t.Helper()
	ctx, skips := azrecs.WithSkipLog(context.Background())
	lookups := map[common.ServiceType]azrecs.PriceFunc{common.ServiceCompute: f.lookup}
	return priceAdvisorReservations(ctx, rows, lookups), skips
}

func TestPriceAdvisorReservations_PricesBothLegsInUSD(t *testing.T) {
	t.Parallel()
	got, skips := priceRows(t, &fakePrices{prices: map[string]azrecs.ReservationPrice{"Standard_D2s_v3": usd(120)}}, vmBase("eastus", "Standard_D2s_v3", 4))
	require.Len(t, got, 2)
	assert.Empty(t, skips.Entries())
	assert.Equal(t, "upfront", got[0].PaymentOption)
	assert.Equal(t, 480.0, got[0].CommitmentCost)
	assert.Equal(t, "monthly", got[1].PaymentOption)
	assert.Equal(t, 40.0, *got[1].RecurringMonthlyCost)
	for _, g := range got {
		assert.Equal(t, "USD", g.CommitmentCurrency)
	}
}

func TestPriceAdvisorReservations_UnpriceableRowsAreSkippedAndCounted(t *testing.T) {
	t.Parallel()
	f := &fakePrices{prices: map[string]azrecs.ReservationPrice{
		"eur": {Total: 100, Currency: "EUR"}, "zero": usd(0),
	}}
	got, skips := priceRows(t, f, vmBase("eastus", "missing", 1), vmBase("eastus", "eur", 1), vmBase("eastus", "zero", 1))
	assert.Empty(t, got)
	assert.Len(t, skips.Entries(), 3)
}

func TestPriceAdvisorReservations_CollectionFailureStopsLookups(t *testing.T) {
	t.Parallel()
	f := &fakePrices{err: fmt.Errorf("get: %w", pricing.ErrFetch)}
	got, skips := priceRows(t, f, vmBase("eastus", "a", 1), vmBase("eastus", "b", 1), vmBase("westus", "c", 1))
	assert.Empty(t, got)
	assert.Equal(t, 1, f.calls, "no further lookups once pricing is unavailable")
	assert.Len(t, skips.Entries(), 3)
}

func TestPriceAdvisorReservations_PassesNonReservationsThrough(t *testing.T) {
	t.Parallel()
	sp := common.Recommendation{CommitmentType: common.CommitmentSavingsPlan, Service: common.ServiceCompute, Account: "sub", Term: "1yr"}
	f := &fakePrices{}
	got, skips := priceRows(t, f, sp)
	assert.Equal(t, []common.Recommendation{sp}, got)
	assert.Zero(t, f.calls)
	assert.Empty(t, skips.Entries())
}

func TestPriceAdvisorReservations_ServiceWithoutPricerIsSkipped(t *testing.T) {
	t.Parallel()
	ctx, skips := azrecs.WithSkipLog(context.Background())
	got := priceAdvisorReservations(ctx, []common.Recommendation{riRow(common.ServiceCache, "eastus", "1yr", "Premium_P1", 1, "")}, nil)
	assert.Empty(t, got)
	require.Len(t, skips.Entries(), 1)
	assert.Contains(t, skips.Entries()[0], "no reservation pricer for service")
}

func TestPriceAdvisorReservations_KeepsCheapestPricedTotalPerNeed(t *testing.T) {
	t.Parallel()
	prices := map[string]azrecs.ReservationPrice{"Standard_D2s_v3": usd(100), "Standard_D4s_v3": usd(250), "Standard_D8s_v3": usd(100)}
	d2 := vmBase("eastus", "Standard_D2s_v3", 4)  // 400
	d4 := vmBase("eastus", "Standard_D4s_v3", 2)  // 500
	tie := vmBase("eastus", "Standard_D8s_v3", 4) // 400, later: first wins
	other := vmBase("westus", "Standard_D4s_v3", 2)

	for name, rows := range map[string][]common.Recommendation{"cheaper first": {d2, d4}, "cheaper last": {d4, d2}} {
		got, skips := priceRows(t, &fakePrices{prices: prices}, rows...)
		require.Len(t, got, 2, name)
		assert.Equal(t, "Standard_D2s_v3", got[0].ResourceType, name)
		assert.Equal(t, "Standard_D2s_v3", got[1].ResourceType, name)
		assert.Empty(t, skips.Entries(), "collapse is not a skip")
	}

	got, _ := priceRows(t, &fakePrices{prices: prices}, d2, tie, other)
	require.Len(t, got, 4)
	assert.Equal(t, "Standard_D2s_v3", got[0].ResourceType, "ties go to the first")
	assert.Equal(t, "westus", got[2].Region, "another region is another need")

	got, skips := priceRows(t, &fakePrices{prices: prices}, vmBase("eastus", "unpriced", 1), d4)
	require.Len(t, got, 2, "a failed row never hides a priced one")
	assert.Len(t, skips.Entries(), 1)
}

func TestPriceAdvisorReservations_CurrencyImpliesBothLegsPriced(t *testing.T) {
	t.Parallel()
	f := &fakePrices{prices: map[string]azrecs.ReservationPrice{"a": usd(100), "eur": {Total: 5, Currency: "EUR"}}}
	got, _ := priceRows(t, f, vmBase("eastus", "a", 2), vmBase("eastus", "eur", 1), vmBase("westus", "a", 1))
	var withCurrency int
	for _, g := range got {
		if g.CommitmentCurrency == "" {
			continue
		}
		withCurrency++
		switch g.PaymentOption {
		case "upfront":
			assert.Positive(t, g.CommitmentCost)
		case "monthly":
			require.NotNil(t, g.RecurringMonthlyCost)
			assert.Positive(t, *g.RecurringMonthlyCost)
		default:
			assert.Fail(t, "unexpected payment option", g.PaymentOption)
		}
	}
	assert.Equal(t, 4, withCurrency, "two priced needs, two legs each")
}

func TestPriceAdvisorReservations_NonBaseRowIsSkipped(t *testing.T) {
	t.Parallel()
	f := &fakePrices{prices: map[string]azrecs.ReservationPrice{"a": usd(100)}}
	row := vmBase("eastus", "a", 1)
	row.PaymentOption = "upfront"
	got, skips := priceRows(t, f, row)
	assert.Empty(t, got)
	assert.Zero(t, f.calls)
	require.Len(t, skips.Entries(), 1)
	assert.Contains(t, skips.Entries()[0], "not a base row")
}
