package cloudsql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func currencySKU(id, description, region, currency string, nanos int64) *cloudbilling.Sku {
	return &cloudbilling.Sku{
		SkuId:          id,
		Description:    description,
		ServiceRegions: []string{region},
		PricingInfo: []*cloudbilling.PricingInfo{{
			PricingExpression: &cloudbilling.PricingExpression{
				TieredRates: []*cloudbilling.TierRate{{
					UnitPrice: &cloudbilling.Money{CurrencyCode: currency, Nanos: nanos},
				}},
			},
		}},
	}
}

func TestGetSQLPricing_Currency(t *testing.T) {
	const tier, region = "db-n1-standard-1", "us-central1"
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
			ctx := context.Background()
			client, err := NewClient(ctx, "test-project", region)
			require.NoError(t, err)
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: []*cloudbilling.Sku{
				currencySKU("od", tier+" Cloud SQL", region, tt.onDemandCur, 50000000),
				currencySKU("cud", tier+" Cloud SQL commitment 1yr", region, tt.commitCur, 42000000),
			}}})

			got, err := client.getSQLPricing(ctx, tier, region, 1)
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
