package cloudsql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// TestExtractPricing_AmbiguousMatches covers issue #97: the extractor used to
// keep whichever matching SKU came last, so catalog order decided the price.
func TestExtractPricing_AmbiguousMatches(t *testing.T) {
	const tier, decoy, region = "db-n1-standard-1", "db-n1-standard-16", "europe-west4"
	od := func(id, tierName string, nanos int64) *cloudbilling.Sku {
		return currencySKU(id, "Cloud SQL for MySQL "+tierName+" in "+region, "OnDemand", region, "USD", nanos)
	}
	cud := func(id, usage string, nanos int64) *cloudbilling.Sku {
		return currencySKU(id, "Cloud SQL for MySQL "+tier+" commitment in "+region, usage, region, "USD", nanos)
	}
	tests := []struct {
		name           string
		skus           []*cloudbilling.Sku
		wantOnDemand   float64
		wantCommitment float64
		wantErr        string
	}{
		{
			name:         "superstring tier SKU after the real one is ignored",
			skus:         []*cloudbilling.Sku{od("od", tier, 50000000), od("od-decoy", decoy, 800000000), cud("cud", "Commit1Yr", 40000000)},
			wantOnDemand: 0.05, wantCommitment: 0.04,
		},
		{
			name:         "superstring tier SKU before the real one is ignored",
			skus:         []*cloudbilling.Sku{od("od-decoy", decoy, 800000000), od("od", tier, 50000000), cud("cud", "Commit1Yr", 40000000)},
			wantOnDemand: 0.05, wantCommitment: 0.04,
		},
		{
			name:         "duplicate on-demand SKUs at the same price agree",
			skus:         []*cloudbilling.Sku{od("od-a", tier, 50000000), od("od-b", tier, 50000000), cud("cud", "Commit1Yr", 40000000)},
			wantOnDemand: 0.05, wantCommitment: 0.04,
		},
		{
			name:    "two on-demand SKUs at different prices are ambiguous",
			skus:    []*cloudbilling.Sku{od("od-a", tier, 50000000), od("od-b", tier, 60000000), cud("cud", "Commit1Yr", 40000000)},
			wantErr: "ambiguous catalog: on-demand SKU od-b",
		},
		{
			name:    "same conflict in reverse order is still ambiguous",
			skus:    []*cloudbilling.Sku{od("od-b", tier, 60000000), od("od-a", tier, 50000000), cud("cud", "Commit1Yr", 40000000)},
			wantErr: "ambiguous catalog: on-demand SKU od-a",
		},
		{
			name:    "1Yr and 3Yr commitment SKUs at different prices are ambiguous",
			skus:    []*cloudbilling.Sku{od("od", tier, 50000000), cud("cud-1y", "Commit1Yr", 40000000), cud("cud-3y", "Commit3Yr", 30000000)},
			wantErr: "ambiguous catalog: commitment SKU cud-3y",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			onDemand, commitment, _, err := extractSQLPricingFromSKUs(tt.skus, tier, region)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.wantOnDemand, onDemand, 1e-12)
			assert.InDelta(t, tt.wantCommitment, commitment, 1e-12)
		})
	}
}
