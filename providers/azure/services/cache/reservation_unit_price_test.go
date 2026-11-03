package cache

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

func TestClient_ReservationUnitPrice(t *testing.T) {
	t.Parallel()
	client := NewClientWithHTTP(nil, "sub", "eastus", &mocks.PricingHTTP{Items: []map[string]any{
		mocks.ReservationRow("Redis Cache", "Azure Redis Cache Premium", "eastus", "Azure_Redis_Cache_Premium_P1_Cache", "P1", "P1 Cache Instance", "1 Year", 500),
	}})

	got, err := client.ReservationUnitPrice(context.Background(), "Premium_P1", "eastus", 1)

	require.NoError(t, err)
	assert.Equal(t, azrecs.ReservationPrice{Total: 500, Currency: "USD"}, got)
}
