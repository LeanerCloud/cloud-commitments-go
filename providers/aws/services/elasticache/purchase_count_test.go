package elasticache

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPurchaseCommitment_Count(t *testing.T) {
	t.Parallel()
	rec := func(n int) common.Recommendation {
		return common.Recommendation{
			Service: common.ServiceCache, ResourceType: "cache.m5.large", Count: n,
			PaymentOption: "all-upfront", Term: "1yr",
			Details: &common.CacheDetails{Engine: "redis", NodeType: "cache.m5.large"},
		}
	}

	t.Run("max int32 is sent exactly", func(t *testing.T) {
		t.Parallel()
		m := &MockElastiCacheClient{}
		m.Test(t)
		m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
			Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{
				ReservedCacheNodesOfferings: []types.ReservedCacheNodesOffering{{
					ReservedCacheNodesOfferingId: aws.String("offering-123"),
					CacheNodeType:                aws.String("cache.m5.large"),
					ProductDescription:           aws.String("redis"),
					OfferingType:                 aws.String("All Upfront"),
					Duration:                     aws.Int32(31536000),
				}},
			}, nil)
		m.On("PurchaseReservedCacheNodesOffering", mock.Anything, mock.MatchedBy(func(in *elasticache.PurchaseReservedCacheNodesOfferingInput) bool {
			return aws.ToInt32(in.CacheNodeCount) == math.MaxInt32
		})).Return(&elasticache.PurchaseReservedCacheNodesOfferingOutput{ReservedCacheNode: &types.ReservedCacheNode{ReservedCacheNodeId: aws.String("rn-1")}}, nil).Once()

		result, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec(math.MaxInt32), common.PurchaseOptions{})
		require.NoError(t, err)
		assert.True(t, result.Success)
		m.AssertNumberOfCalls(t, "PurchaseReservedCacheNodesOffering", 1)
	})

	for _, n := range []int{math.MaxInt32 + 1, 0, -1} {
		t.Run(fmt.Sprintf("refused before any API call, count %d", n), func(t *testing.T) {
			t.Parallel()
			m := &MockElastiCacheClient{}
			m.Test(t)
			opts := common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-count", 0)}

			result, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec(n), opts)
			require.Error(t, err)
			assert.False(t, result.Success)
			assert.ErrorIs(t, err, offeringprice.ErrPurchaseCountOutOfRange)
			assert.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
			assert.Empty(t, m.Calls, "a bad count must make no describe and no purchase call")
		})
	}
}
