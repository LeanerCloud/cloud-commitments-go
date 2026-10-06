package memorystore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func usageTypeSKU(description, usageType string, nanos int32) *cloudbilling.Sku {
	return &cloudbilling.Sku{
		SkuId:          "sku-" + usageType,
		Description:    description,
		Category:       &cloudbilling.Category{UsageType: usageType},
		ServiceRegions: []string{"us-central1"},
		PricingInfo: []*cloudbilling.PricingInfo{{
			PricingExpression: &cloudbilling.PricingExpression{
				TieredRates: []*cloudbilling.TierRate{{
					UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: int64(nanos)},
				}},
			},
		}},
	}
}

func TestExtractor_UsageTypeDecidesCommitmentSlot(t *testing.T) {
	skus := []*cloudbilling.Sku{
		usageTypeSKU("Memorystore Redis STANDARD_HA", "OnDemand", 50000000),
		usageTypeSKU("Memorystore Redis STANDARD_HA Committed Use Discount", "Commit1Yr", 42000000),
	}
	onDemand, commitment, _, err := extractPricingFromSKUs(skus, "STANDARD_HA", "us-central1")
	require.NoError(t, err)
	assert.InDelta(t, 0.05, onDemand, 1e-9)
	assert.InDelta(t, 0.042, commitment, 1e-9)
}

func TestExtractor_UnknownUsageTypeIsAnError(t *testing.T) {
	skus := []*cloudbilling.Sku{usageTypeSKU("Memorystore Redis STANDARD_HA", "Commit5Yr", 42000000)}
	_, _, _, err := extractPricingFromSKUs(skus, "STANDARD_HA", "us-central1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized usage type")
}
