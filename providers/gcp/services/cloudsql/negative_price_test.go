package cloudsql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func negPriceSkus(onDemandNanos, commitNanos int64) []*cloudbilling.Sku {
	sku := func(desc, usage string, nanos int64) *cloudbilling.Sku {
		return &cloudbilling.Sku{
			Description:    desc,
			Category:       &cloudbilling.Category{UsageType: usage},
			ServiceRegions: []string{"us-central1"},
			PricingInfo: []*cloudbilling.PricingInfo{{PricingExpression: &cloudbilling.PricingExpression{
				TieredRates: []*cloudbilling.TierRate{{UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: nanos}}},
			}}},
		}
	}
	return []*cloudbilling.Sku{
		sku("db-n1-standard-1 Cloud SQL", "OnDemand", onDemandNanos),
		sku("db-n1-standard-1 Cloud SQL commitment", "Commit1Yr", commitNanos),
	}
}

// A negative price would flip the sign of the savings percentage, so pricing
// must fail closed instead of reporting it.
func TestGetSQLPricing_RejectsNegativePrices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		onDemand, commit int64
		wantErr          string
	}{
		{"negative on-demand", -50000000, 42000000, "no pricing found"},
		{"negative commitment", 50000000, -42000000, "no commitment pricing found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client, err := NewClient(ctx, "test-project", "us-central1")
			require.NoError(t, err)
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: negPriceSkus(tt.onDemand, tt.commit)}})

			pricing, err := client.getSQLPricing(ctx, "db-n1-standard-1", "us-central1", 1)
			require.Error(t, err)
			assert.Nil(t, pricing)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
