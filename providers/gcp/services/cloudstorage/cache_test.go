package cloudstorage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func TestGetStoragePricing_CachesSKUsPerClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := &countingBilling{BillingService: &MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: storageMockSkus("STANDARD", 26000000, 19500000)}}}
	c, err := NewClient(ctx, "p", "us-central1")
	require.NoError(t, err)
	c.SetBillingService(b)
	for i := 0; i < 3; i++ {
		_, err := c.getStoragePricing(ctx, "STANDARD", "us-central1", 1)
		require.NoError(t, err)
	}
	assert.Equal(t, 1, b.calls)
}
