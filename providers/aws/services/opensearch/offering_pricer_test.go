package opensearch

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

func TestClient_ImplementsOfferingPricer(t *testing.T) {
	t.Parallel()
	mockOS := &MockOpenSearchClient{}
	client := &Client{client: mockOS, region: "us-east-1"}
	rec := common.Recommendation{
		Service: common.ServiceSearch, ResourceType: "m5.large.search",
		PaymentOption: "partial-upfront", Term: "1yr",
		Details: common.SearchDetails{InstanceType: "m5.large.search"},
	}
	mockOS.On("DescribeReservedInstanceOfferings", mock.Anything, mock.Anything).
		Return(&opensearch.DescribeReservedInstanceOfferingsOutput{
			ReservedInstanceOfferings: []types.ReservedInstanceOffering{{
				ReservedInstanceOfferingId: aws.String("offering-123"),
				InstanceType:               types.OpenSearchPartitionInstanceTypeM5LargeSearch,
				Duration:                   31536000,
				PaymentOption:              types.ReservedInstancePaymentOptionPartialUpfront,
				FixedPrice:                 aws.Float64(3000.0),
				UsagePrice:                 aws.Float64(0.15),
				CurrencyCode:               aws.String("USD"),
			}},
		}, nil)

	var sc provider.ServiceClient = client
	pricer, ok := sc.(provider.OfferingPricer)
	require.True(t, ok, "client must implement provider.OfferingPricer")

	got, err := pricer.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	want, err := client.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want, got)
	assert.Equal(t, "offering-123", got.OfferingID)
}
