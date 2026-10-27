package cloudsql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// Pins the service label and region that ScaleToTerm puts in its errors.
func TestGetSQLPricing_ScaleErrorsNameLabelAndRegion(t *testing.T) {
	t.Parallel()
	const region = "europe-west4"
	tests := []struct {
		name             string
		onDemand, commit int64
		wantErr          string
	}{
		{"no on-demand", -50000000, 42000000, "no pricing found for Cloud SQL tier db-n1-standard-1"},
		{"no commitment", 50000000, -42000000, "no commitment pricing found for Cloud SQL tier db-n1-standard-1 in region europe-west4: catalog has no CUD SKU; cannot compute savings percentage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client, err := NewClient(ctx, "test-project", region)
			require.NoError(t, err)
			skus := negPriceSkus(tt.onDemand, tt.commit)
			for _, sku := range skus {
				sku.ServiceRegions = []string{region}
			}
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: skus}})

			_, err = client.getSQLPricing(ctx, "db-n1-standard-1", region, 1)
			assert.EqualError(t, err, tt.wantErr)
		})
	}
}
