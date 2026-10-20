package cloudsql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// Pricing N recommendations lists the SKU catalog once.
type countingBilling struct {
	BillingService
	calls int
}

func (b *countingBilling) ListSKUs(id string) (*cloudbilling.ListSkusResponse, error) {
	b.calls++
	return b.BillingService.ListSKUs(id)
}

func TestListSKUs_CachedPerClient(t *testing.T) {
	t.Parallel()
	b := &countingBilling{BillingService: &MockBillingService{skus: &cloudbilling.ListSkusResponse{}}}
	c, _ := NewClient(context.Background(), "p", "us-central1")
	for i := 0; i < 3; i++ {
		_, err := c.listSKUs(b)
		require.NoError(t, err)
	}
	assert.Equal(t, 1, b.calls)
}
