package memorystore

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// freeFirstTierSKU returns a SKU whose first tier is a free allowance and whose
// second tier carries the paid rate (issue #242).
func freeFirstTierSKU(id, description, usageType, region string, paidNanos int64) *cloudbilling.Sku {
	sku := currencySKU(id, description, usageType, region, "USD", 0)
	sku.PricingInfo[0].PricingExpression.TieredRates = append(sku.PricingInfo[0].PricingExpression.TieredRates,
		&cloudbilling.TierRate{StartUsageAmount: 100, UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: paidNanos}})
	return sku
}

func TestExtractPricing_FreeFirstTierUsesPaidTier(t *testing.T) {
	t.Parallel()
	const tier, region = "STANDARD_HA", "us-central1"
	od := freeFirstTierSKU("od", tier+" Memorystore", "OnDemand", region, 50000000)

	price, currency := skumatch.UnitPrice(od)
	assert.InDelta(t, 0.05, price, 1e-12)
	assert.Equal(t, "USD", currency)

	onDemand, commitment, currency, err := extractPricingFromSKUs([]*cloudbilling.Sku{
		od,
		freeFirstTierSKU("cud", tier+" Memorystore commitment 1yr", "Commit1Yr", region, 42000000),
	}, tier, region, 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.05, onDemand, 1e-12)
	assert.InDelta(t, 0.042, commitment, 1e-12)
	assert.Equal(t, "USD", currency)
}

func TestExtractPricing_AllTiersFreeIsSkipped(t *testing.T) {
	t.Parallel()
	sku := freeFirstTierSKU("od", "STANDARD_HA Memorystore", "OnDemand", "us-central1", 0)
	price, _ := skumatch.UnitPrice(sku)
	assert.Zero(t, price)
}
