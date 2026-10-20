package cloudstorage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// freeFirstTierStorageSKU returns a SKU whose first tier is a free allowance and
// whose second tier carries the paid rate (issue #242).
func freeFirstTierStorageSKU(description, usageType string, paidNanos int64) *cloudbilling.Sku {
	sku := usageTypeSKU(description, usageType, 0)
	sku.PricingInfo[0].PricingExpression.TieredRates = append(sku.PricingInfo[0].PricingExpression.TieredRates,
		&cloudbilling.TierRate{StartUsageAmount: 5, UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: paidNanos}})
	return sku
}

func TestExtractStoragePricing_FreeFirstTierUsesPaidTier(t *testing.T) {
	t.Parallel()
	od := freeFirstTierStorageSKU("STANDARD Storage in us-central1", "OnDemand", 20000000)

	price, currency, err := extractStoragePriceFromSKU(od)
	require.NoError(t, err)
	assert.InDelta(t, 0.02/averageHoursPerMonth, price, 1e-15)
	assert.Equal(t, "USD", currency)

	onDemand, commitment, currency, err := extractStoragePricingFromSKUs([]*cloudbilling.Sku{
		od,
		freeFirstTierStorageSKU("STANDARD Storage in us-central1 Committed Use Discount", "Commit1Yr", 16000000),
	}, "STANDARD", "us-central1")
	require.NoError(t, err)
	assert.InDelta(t, 0.02/averageHoursPerMonth, onDemand, 1e-15)
	assert.InDelta(t, 0.016/averageHoursPerMonth, commitment, 1e-15)
	assert.Equal(t, "USD", currency)
}

func TestExtractStoragePricing_AllTiersFreeIsSkipped(t *testing.T) {
	t.Parallel()
	sku := freeFirstTierStorageSKU("STANDARD Storage in us-central1", "OnDemand", 0)
	price, _, err := extractStoragePriceFromSKU(sku)
	require.NoError(t, err)
	assert.Zero(t, price)
}
