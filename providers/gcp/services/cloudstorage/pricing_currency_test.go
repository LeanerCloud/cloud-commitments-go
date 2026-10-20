package cloudstorage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func currencySKU(id, description, usageType, region, currency string, nanos int64) *cloudbilling.Sku {
	return &cloudbilling.Sku{
		SkuId:          id,
		Description:    description,
		Category:       &cloudbilling.Category{UsageType: usageType},
		ServiceRegions: []string{region},
		PricingInfo: []*cloudbilling.PricingInfo{{
			PricingExpression: &cloudbilling.PricingExpression{
				UsageUnit: "GiBy.mo",
				TieredRates: []*cloudbilling.TierRate{{
					UnitPrice: &cloudbilling.Money{CurrencyCode: currency, Nanos: nanos},
				}},
			},
		}},
	}
}

func TestGetStoragePricing_Currency(t *testing.T) {
	t.Parallel()
	const storageClass, region = "STANDARD", "us-central1"
	tests := []struct {
		name         string
		onDemandCur  string
		commitCur    string
		wantCurrency string
		wantErr      string
	}{
		{name: "consistent non-USD currency is returned as is", onDemandCur: "EUR", commitCur: "EUR", wantCurrency: "EUR"},
		{name: "consistent USD", onDemandCur: "USD", commitCur: "USD", wantCurrency: "USD"},
		{name: "on-demand and commitment currencies differ", onDemandCur: "USD", commitCur: "EUR", wantErr: "priced in EUR but earlier SKUs are priced in USD"},
		{name: "commitment currency missing", onDemandCur: "EUR", commitCur: "", wantErr: "no currency code"},
		{name: "on-demand currency missing", onDemandCur: "", commitCur: "EUR", wantErr: "no currency code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client, err := NewClient(ctx, "test-project", region)
			require.NoError(t, err)
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: []*cloudbilling.Sku{
				currencySKU("od", storageClass+" Storage in "+region, "OnDemand", region, tt.onDemandCur, 26000000),
				currencySKU("cud", storageClass+" Storage commitment in "+region, "Commit1Yr", region, tt.commitCur, 19500000),
			}}})

			got, err := client.getStoragePricing(ctx, storageClass, region, 1)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantCurrency, got.Currency)
		})
	}
}
