package cloudstorage

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
				UsageUnit: "GiBy.mo",
				TieredRates: []*cloudbilling.TierRate{{
					UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: int64(nanos)},
				}},
			},
		}},
	}
}

func TestExtractor_UsageTypeDecidesCommitmentSlot(t *testing.T) {
	skus := []*cloudbilling.Sku{
		usageTypeSKU("STANDARD Storage in us-central1", "OnDemand", 50000000),
		usageTypeSKU("STANDARD Storage in us-central1 Committed Use Discount", "Commit1Yr", 42000000),
	}
	onDemand, commitment, _, err := extractStoragePricingFromSKUs(skus, "STANDARD", "us-central1")
	require.NoError(t, err)
	assert.Greater(t, onDemand, commitment)
	assert.Greater(t, commitment, 0.0)
}

func TestExtractor_UnknownUsageTypeIsAnError(t *testing.T) {
	skus := []*cloudbilling.Sku{usageTypeSKU("STANDARD Storage in us-central1", "Commit5Yr", 42000000)}
	_, _, _, err := extractStoragePricingFromSKUs(skus, "STANDARD", "us-central1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized usage type")
}
