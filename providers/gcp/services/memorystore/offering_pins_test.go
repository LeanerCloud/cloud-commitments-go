package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Pins the offering ID prefix and that the hourly rate and the term total are
// not swapped.
func TestMemorystoreClient_GetOfferingDetails_PinsIDRateAndTotal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, err := NewClient(ctx, "test-project", "us-central1")
	require.NoError(t, err)
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: negPriceSkus(50000000, 42000000)}})

	details, err := client.GetOfferingDetails(ctx, common.Recommendation{ResourceType: "STANDARD_HA", Term: "1yr", PaymentOption: "all-upfront"})
	require.NoError(t, err)
	assert.Equal(t, "gcp-memorystore-STANDARD_HA-us-central1-1yr", details.OfferingID)
	assert.InDelta(t, 0.042, details.EffectiveHourlyRate, 1e-9)
	assert.InDelta(t, 0.042*8760, details.TotalCost, 1e-6)
}
