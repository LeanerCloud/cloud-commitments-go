package cloudsql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Pins the "failed to get pricing" wrap that GetOfferingDetails puts around a
// pricing failure, so the pricing pipeline can move without changing it.
func TestCloudSQLClient_GetOfferingDetails_WrapsPricingError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, err := NewClient(ctx, "test-project", "us-central1")
	require.NoError(t, err)
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{}})

	_, err = client.GetOfferingDetails(ctx, common.Recommendation{ResourceType: "db-n1-standard-1", Term: "1yr", PaymentOption: "all-upfront"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get pricing: ")
}
