package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// A catalog that only lists 1-year commitment SKUs must not price a 3-year
// request (#254): the missing term is an error, not a zero price.
func TestMemorystoreClient_GetOfferingDetails_3yrWithOnly1yrCatalogErrors(t *testing.T) {
	t.Parallel()
	const region = "europe-west1"
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", region)
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: []*cloudbilling.Sku{
		currencySKU("od", "redis-basic Cloud Memorystore Redis", "OnDemand", region, "EUR", 200000000),
		currencySKU("cud", "redis-basic Cloud Memorystore Redis commitment 1yr", "Commit1Yr", region, "EUR", 137000000),
	}}})

	details, err := client.GetOfferingDetails(ctx, common.Recommendation{ResourceType: "redis-basic", Region: region, Term: "3yr", PaymentOption: "monthly"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no commitment pricing found")
	assert.Nil(t, details)
}
