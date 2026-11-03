package compute

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
		mocks.ReservationRow("Virtual Machines", "Virtual Machines D Series", "eastus", "Standard_D2s_v3", "D2s v3", "D2s v3", "1 Year", 500),
	}})

	got, err := client.ReservationUnitPrice(context.Background(), "Standard_D2s_v3", "eastus", 1)

	require.NoError(t, err)
	assert.Equal(t, azrecs.ReservationPrice{Total: 500, Currency: "USD"}, got)
}
