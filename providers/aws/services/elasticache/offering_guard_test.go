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
)

func guardRec(engine string) common.Recommendation {
	return common.Recommendation{
		ResourceType: "cache.r6g.large", PaymentOption: "no-upfront", Term: "1yr",
		Details: &common.CacheDetails{Engine: engine},
	}
}

func guardOffering(mut func(*types.ReservedCacheNodesOffering)) types.ReservedCacheNodesOffering {
	o := types.ReservedCacheNodesOffering{
		ReservedCacheNodesOfferingId: aws.String("off"),
		CacheNodeType:                aws.String("cache.r6g.large"),
		Duration:                     aws.Int32(31536000),
		ProductDescription:           aws.String("redis"),
		OfferingType:                 aws.String("No Upfront"),
	}
	if mut != nil {
		mut(&o)
	}
	return o
}

// CE can omit ProductDescription; the lookup must name the missing field
// instead of sending an empty engine filter.
func TestFindOfferingID_EmptyEngineRefusedBeforeAPICall(t *testing.T) {
	t.Parallel()
	for _, engine := range []string{"", "  "} {
		mockEC := &MockElastiCacheClient{}
		client := &Client{client: mockEC, region: "us-east-1"}
		_, err := client.findOfferingID(context.Background(), guardRec(engine), "")
		require.ErrorContains(t, err, "no engine", "engine %q", engine)
		mockEC.AssertNotCalled(t, "DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything)
	}
}

// ElastiCache reports lowercase engines; a title-cased CE value must be sent
// lowercased and still match the lowercase offering.
func TestFindOfferingID_EngineFilterIsLowercased(t *testing.T) {
	t.Parallel()
	mockEC := &MockElastiCacheClient{}
	client := &Client{client: mockEC, region: "us-east-1"}
	mockEC.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.MatchedBy(func(in *elasticache.DescribeReservedCacheNodesOfferingsInput) bool {
		return aws.ToString(in.ProductDescription) == "redis"
	})).Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{
		ReservedCacheNodesOfferings: []types.ReservedCacheNodesOffering{guardOffering(nil)},
	}, nil).Once()
	id, err := client.findOfferingID(context.Background(), guardRec(" Redis "), "")
	require.NoError(t, err)
	assert.Equal(t, "off", id)
	mockEC.AssertExpectations(t)
}

// The request filters these fields, but an offering that differs on any of
// them must not be bought just because it is first on the page.
func TestFindOfferingID_RejectsOfferingForWrongNodeDurationOrEngine(t *testing.T) {
	t.Parallel()
	for name, mut := range map[string]func(*types.ReservedCacheNodesOffering){
		"node type":   func(o *types.ReservedCacheNodesOffering) { o.CacheNodeType = aws.String("cache.m6g.large") },
		"duration":    func(o *types.ReservedCacheNodesOffering) { o.Duration = aws.Int32(94608000) },
		"engine":      func(o *types.ReservedCacheNodesOffering) { o.ProductDescription = aws.String("memcached") },
		"no engine":   func(o *types.ReservedCacheNodesOffering) { o.ProductDescription = nil },
		"no duration": func(o *types.ReservedCacheNodesOffering) { o.Duration = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mockEC := &MockElastiCacheClient{}
			client := &Client{client: mockEC, region: "us-east-1"}
			mockEC.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
				Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{
					ReservedCacheNodesOfferings: []types.ReservedCacheNodesOffering{guardOffering(mut)},
				}, nil).Once()
			_, err := client.findOfferingID(context.Background(), guardRec("redis"), "")
			require.ErrorContains(t, err, "API filter mismatch")
		})
	}
}
