package cloudstorage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// A negative price would flip the sign of the savings percentage, so pricing
// must fail closed instead of reporting it.
func TestGetStoragePricing_RejectsNegativePrices(t *testing.T) {
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
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: storageMockSkus("STANDARD", tt.onDemand, tt.commit)}})

			pricing, err := client.getStoragePricing(ctx, "STANDARD", "us-central1", 1)
			require.Error(t, err)
			assert.Nil(t, pricing)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
