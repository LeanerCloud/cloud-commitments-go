package memorystore

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// TestExtractPricing_CommitmentTerm covers issue #254: 1-year and 3-year
// commitment SKUs priced differently must not be conflated.
func TestExtractPricing_CommitmentTerm(t *testing.T) {
	const tier, region = "STANDARD", "europe-west4"
	sku := func(id, usage string, nanos int64) *cloudbilling.Sku {
		return currencySKU(id, "Redis Capacity "+tier+" in "+region, usage, region, "USD", nanos)
	}
	od, y1, y3 := sku("od", "OnDemand", 50000000), sku("y1", "Commit1Yr", 40000000), sku("y3", "Commit3Yr", 30000000)
	y1dup := sku("y1-dup", "Commit1Yr", 39000000)
	orders := map[string][]*cloudbilling.Sku{"1yr first": {od, y1, y3}, "3yr first": {od, y3, y1}}

	for order, skus := range orders {
		for _, tc := range []struct {
			term int
			want float64
		}{{1, 0.04}, {3, 0.03}} {
			t.Run(fmt.Sprintf("%s/%dyr", order, tc.term), func(t *testing.T) {
				onDemand, commitment, _, err := extractPricingFromSKUs(skus, tier, region, tc.term)
				require.NoError(t, err)
				assert.InDelta(t, 0.05, onDemand, 1e-12)
				assert.InDelta(t, tc.want, commitment, 1e-12)
			})
		}
	}
	t.Run("same-term duplicate at different prices is ambiguous", func(t *testing.T) {
		_, _, _, err := extractPricingFromSKUs([]*cloudbilling.Sku{od, y1, y3, y1dup}, tier, region, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous catalog: 1-year commitment SKU y1-dup")
	})
	t.Run("only the other term present yields no commitment", func(t *testing.T) {
		_, commitment, _, err := extractPricingFromSKUs([]*cloudbilling.Sku{od, y1}, tier, region, 3)
		require.NoError(t, err)
		assert.Zero(t, commitment)
	})
	t.Run("unknown term is an error", func(t *testing.T) {
		_, _, _, err := extractPricingFromSKUs([]*cloudbilling.Sku{od, y1, y3}, tier, region, 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported commitment term")
	})
}
