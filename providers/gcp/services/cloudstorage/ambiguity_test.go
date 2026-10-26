package cloudstorage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func storageSKU(id, description, usageType string, regions []string, nanos int64) *cloudbilling.Sku {
	return &cloudbilling.Sku{
		SkuId:          id,
		Description:    description,
		Category:       &cloudbilling.Category{UsageType: usageType},
		ServiceRegions: regions,
		PricingInfo: []*cloudbilling.PricingInfo{{
			PricingExpression: &cloudbilling.PricingExpression{
				UsageUnit: "GiBy.mo",
				TieredRates: []*cloudbilling.TierRate{{
					UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: nanos},
				}},
			},
		}},
	}
}

// TestExtractStoragePricing_RegionAndAmbiguity covers issue #97: a SKU with no
// region list used to price every region, and the last matching SKU won.
func TestExtractStoragePricing_RegionAndAmbiguity(t *testing.T) {
	const class, region = "STANDARD", "europe-west4"
	here := []string{region}
	tests := []struct {
		name           string
		skus           []*cloudbilling.Sku
		wantOnDemand   float64
		wantCommitment float64
		wantErr        string
	}{
		{
			name: "region-less SKU of unknown scope does not override the regional price",
			skus: []*cloudbilling.Sku{
				storageSKU("od", "Standard Storage Netherlands", "OnDemand", here, 20000000),
				storageSKU("od-nil", "Standard Storage Taiwan", "OnDemand", nil, 30000000),
				storageSKU("cud", "Standard Storage Netherlands commitment", "Commit1Yr", here, 15000000),
				storageSKU("cud-nil", "Standard Storage Taiwan commitment", "Commit1Yr", nil, 25000000),
			},
			wantOnDemand:   0.02 / averageHoursPerMonth,
			wantCommitment: 0.015 / averageHoursPerMonth,
		},
		{
			name: "region-less SKU alone yields no price",
			skus: []*cloudbilling.Sku{
				storageSKU("od-nil", "Standard Storage Taiwan", "OnDemand", nil, 30000000),
			},
			wantOnDemand: 0,
		},
		{
			name: "two regional SKUs at different prices are ambiguous",
			skus: []*cloudbilling.Sku{
				storageSKU("od-a", "Standard Storage Netherlands", "OnDemand", here, 20000000),
				storageSKU("od-b", "Standard Storage Netherlands", "OnDemand", here, 23000000),
			},
			wantErr: "ambiguous catalog: on-demand SKU od-b",
		},
		{
			name: "two regional SKUs at the same price agree",
			skus: []*cloudbilling.Sku{
				storageSKU("od-a", "Standard Storage Netherlands", "OnDemand", here, 20000000),
				storageSKU("od-b", "Standard Storage Netherlands", "OnDemand", here, 20000000),
			},
			wantOnDemand: 0.02 / averageHoursPerMonth,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			onDemand, commitment, _, err := extractStoragePricingFromSKUs(tt.skus, class, region, 1)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.wantOnDemand, onDemand, 1e-15)
			assert.InDelta(t, tt.wantCommitment, commitment, 1e-15)
		})
	}
}
