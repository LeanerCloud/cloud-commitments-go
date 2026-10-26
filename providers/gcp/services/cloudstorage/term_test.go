package cloudstorage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// TestExtractStoragePricing_CommitmentTerm covers issue #254: 1-year and
// 3-year commitment SKUs priced differently must not be conflated.
func TestExtractStoragePricing_CommitmentTerm(t *testing.T) {
	const class, region = "STANDARD", "europe-west4"
	here := []string{region}
	sku := func(id, usage string, nanos int64) *cloudbilling.Sku {
		return storageSKU(id, "Standard Storage Netherlands", usage, here, nanos)
	}
	od, y1, y3 := sku("od", "OnDemand", 20000000), sku("y1", "Commit1Yr", 15000000), sku("y3", "Commit3Yr", 10000000)
	y1dup := sku("y1-dup", "Commit1Yr", 14000000)
	orders := map[string][]*cloudbilling.Sku{"1yr first": {od, y1, y3}, "3yr first": {od, y3, y1}}

	for order, skus := range orders {
		for _, tc := range []struct {
			term int
			want float64
		}{{1, 0.015}, {3, 0.010}} {
			t.Run(fmt.Sprintf("%s/%dyr", order, tc.term), func(t *testing.T) {
				onDemand, commitment, _, err := extractStoragePricingFromSKUs(skus, class, region, tc.term)
				require.NoError(t, err)
				assert.InDelta(t, 0.02/common.HoursPerMonth, onDemand, 1e-15)
				assert.InDelta(t, tc.want/common.HoursPerMonth, commitment, 1e-15)
			})
		}
	}
	t.Run("same-term duplicate at different prices is ambiguous", func(t *testing.T) {
		_, _, _, err := extractStoragePricingFromSKUs([]*cloudbilling.Sku{od, y1, y3, y1dup}, class, region, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous catalog: 1-year commitment SKU y1-dup")
	})
	t.Run("only the other term present yields no commitment", func(t *testing.T) {
		_, commitment, _, err := extractStoragePricingFromSKUs([]*cloudbilling.Sku{od, y1}, class, region, 3)
		require.NoError(t, err)
		assert.Zero(t, commitment)
	})
	t.Run("unknown term is an error", func(t *testing.T) {
		_, _, _, err := extractStoragePricingFromSKUs([]*cloudbilling.Sku{od, y1, y3}, class, region, 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported commitment term")
	})
}

// An unsupported term must error instead of being priced as one year (#254).
func TestCloudStorageClient_GetOfferingDetails_UnsupportedTermErrors(t *testing.T) {
	t.Parallel()
	skus := storageMockSkus("STANDARD", 26000000, 19500000)
	commit3 := *skus[1]
	commit3.Category = &cloudbilling.Category{UsageType: "Commit3Yr"}
	commit3.PricingInfo = []*cloudbilling.PricingInfo{{PricingExpression: &cloudbilling.PricingExpression{
		UsageUnit:   "GiBy.mo",
		TieredRates: []*cloudbilling.TierRate{{UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: 15000000}}},
	}}}
	client := storageCatalogClient(t, append(skus, &commit3))

	details, err := client.GetOfferingDetails(t.Context(), common.Recommendation{ResourceType: "STANDARD", Term: "2yr", PaymentOption: "monthly"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported commitment term")
	assert.Nil(t, details)
}
