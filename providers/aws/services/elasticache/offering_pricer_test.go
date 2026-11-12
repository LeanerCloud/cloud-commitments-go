package elasticache

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

func TestClient_ImplementsOfferingPricer(t *testing.T) {
	t.Parallel()
	mockEC := &MockElastiCacheClient{}
	client := &Client{client: mockEC, region: "us-east-2"}
	rec := common.Recommendation{
		Service: common.ServiceCache, ResourceType: "cache.r6g.xlarge",
		PaymentOption: "all-upfront", Term: "1yr",
		Details: &common.CacheDetails{Engine: "redis", NodeType: "cache.r6g.xlarge"},
	}
	mockEC.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{
			ReservedCacheNodesOfferings: []types.ReservedCacheNodesOffering{{
				ReservedCacheNodesOfferingId: aws.String("offering-999"),
				CacheNodeType:                aws.String("cache.r6g.xlarge"),
				Duration:                     aws.Int32(31536000),
				OfferingType:                 aws.String("All Upfront"),
				ProductDescription:           aws.String("redis"),
				FixedPrice:                   aws.Float64(2800.0),
				RecurringCharges:             []types.RecurringCharge{},
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
	assert.Equal(t, "offering-999", got.OfferingID)
}
