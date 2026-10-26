package skumatch

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func pricedSKU(id, usageType, currency string, units, nanos int64) *cloudbilling.Sku {
	return &cloudbilling.Sku{
		SkuId:    id,
		Category: &cloudbilling.Category{UsageType: usageType},
		PricingInfo: []*cloudbilling.PricingInfo{{PricingExpression: &cloudbilling.PricingExpression{
			TieredRates: []*cloudbilling.TierRate{{UnitPrice: &cloudbilling.Money{CurrencyCode: currency, Units: units, Nanos: nanos}}},
		}}},
	}
}

func matchID(ids ...string) func(*cloudbilling.Sku) bool {
	return func(sku *cloudbilling.Sku) bool {
		for _, id := range ids {
			if sku.SkuId == id {
				return true
			}
		}
		return false
	}
}

func TestExtractPricing(t *testing.T) {
	t.Parallel()
	od := pricedSKU("od", "OnDemand", "USD", 0, 100_000_000)
	y1 := pricedSKU("y1", "Commit1Yr", "USD", 0, 60_000_000)
	y3 := pricedSKU("y3", "Commit3Yr", "USD", 0, 40_000_000)
	free := pricedSKU("free", "OnDemand", "USD", 0, 0)
	free.PricingInfo[0].PricingExpression.TieredRates[0].UnitPrice = &cloudbilling.Money{}
	other := pricedSKU("other", "OnDemand", "USD", 9, 0)
	eur := pricedSKU("eur", "Commit1Yr", "EUR", 0, 60_000_000)
	noCurrency := pricedSKU("nocur", "Commit1Yr", "", 0, 60_000_000)
	noCat := pricedSKU("nocat", "", "USD", 0, 1)
	noCat.Category = nil
	od2 := pricedSKU("od2", "OnDemand", "USD", 0, 200_000_000)
	hourly := func(sku *cloudbilling.Sku) (float64, string, error) { p, c := UnitPrice(sku); return p, c, nil }

	all := []*cloudbilling.Sku{od, y1, y3, free, other}
	tests := []struct {
		name          string
		skus          []*cloudbilling.Sku
		match         func(*cloudbilling.Sku) bool
		price         PriceFunc
		term          int
		wantOD, wantC float64
		wantErr       string
	}{
		{name: "1yr", skus: all, match: matchID("od", "y1", "y3", "free"), price: hourly, term: 1, wantOD: 0.1, wantC: 0.06},
		{name: "3yr", skus: all, match: matchID("od", "y1", "y3"), price: hourly, term: 3, wantOD: 0.1, wantC: 0.04},
		{name: "match filter excludes SKUs", skus: all, match: matchID("y1"), price: hourly, term: 1, wantC: 0.06},
		{name: "unsupported term", skus: all, match: matchID("od"), price: hourly, term: 2, wantErr: "unsupported commitment term"},
		{name: "mixed currency", skus: []*cloudbilling.Sku{od, eur}, match: matchID("od", "eur"), price: hourly, term: 1, wantErr: "priced in EUR"},
		{name: "missing currency", skus: []*cloudbilling.Sku{noCurrency}, match: matchID("nocur"), price: hourly, term: 1, wantErr: "no currency code"},
		{name: "unclassifiable SKU", skus: []*cloudbilling.Sku{noCat}, match: matchID("nocat"), price: hourly, term: 1, wantErr: "has no category"},
		{name: "ambiguous slot", skus: []*cloudbilling.Sku{od, od2}, match: matchID("od", "od2"), price: hourly, term: 1, wantErr: "ambiguous catalog"},
		{name: "price error is returned", skus: []*cloudbilling.Sku{od}, match: matchID("od"), term: 1, wantErr: "boom",
			price: func(*cloudbilling.Sku) (float64, string, error) { return 0, "", errors.New("boom") }},
		{name: "price error on unmatched SKU is not reached", skus: []*cloudbilling.Sku{od}, match: matchID(), term: 1,
			price: func(*cloudbilling.Sku) (float64, string, error) { return 0, "", errors.New("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			onDemand, commitment, currency, err := ExtractPricing(tt.skus, tt.match, tt.price, tt.term)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.wantOD, onDemand, 1e-12)
			assert.InDelta(t, tt.wantC, commitment, 1e-12)
			if tt.wantOD > 0 || tt.wantC > 0 {
				assert.Equal(t, "USD", currency)
			}
		})
	}
}

func TestHourlyUnitPrice(t *testing.T) {
	t.Parallel()
	price, currency, err := HourlyUnitPrice(pricedSKU("a", "OnDemand", "USD", 1, 500_000_000))
	require.NoError(t, err)
	assert.InDelta(t, 1.5, price, 1e-12)
	assert.Equal(t, "USD", currency)

	price, currency, err = HourlyUnitPrice(&cloudbilling.Sku{})
	require.NoError(t, err)
	assert.Zero(t, price)
	assert.Empty(t, currency)
}
