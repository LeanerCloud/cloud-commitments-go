package elasticache

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg/purchasetest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/mock"
)

// optRecordingElastiCache records the RetryMaxAttempts the per-operation options set.
type optRecordingElastiCache struct {
	*MockElastiCacheClient
	attempts int
}

func (r *optRecordingElastiCache) PurchaseReservedCacheNodesOffering(ctx context.Context, p *elasticache.PurchaseReservedCacheNodesOfferingInput, optFns ...func(*elasticache.Options)) (*elasticache.PurchaseReservedCacheNodesOfferingOutput, error) {
	var o elasticache.Options
	for _, f := range optFns {
		f(&o)
	}
	r.attempts = o.RetryMaxAttempts
	return r.MockElastiCacheClient.PurchaseReservedCacheNodesOffering(ctx, p, optFns...)
}

func TestPurchaseCommitment_EmptyToken(t *testing.T) {
	t.Parallel()
	call := func(t *testing.T, token string, apiErr error, emptyResp bool) (int, common.PurchaseResult, error) {
		m := &MockElastiCacheClient{}
		rec := common.Recommendation{
			Service: common.ServiceCache, ResourceType: "cache.m5.large", Count: 1,
			PaymentOption: "all-upfront", Term: "1yr",
			Details: &common.CacheDetails{Engine: "redis", NodeType: "cache.m5.large"},
		}
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
		m.On("DescribeReservedCacheNodes", mock.Anything, mock.Anything).
			Return(&elasticache.DescribeReservedCacheNodesOutput{}, nil)
		out := purchasetest.Pick(&elasticache.PurchaseReservedCacheNodesOfferingOutput{ReservedCacheNode: &types.ReservedCacheNode{ReservedCacheNodeId: aws.String("rn-1")}},
			&elasticache.PurchaseReservedCacheNodesOfferingOutput{}, apiErr, emptyResp)
		m.On("PurchaseReservedCacheNodesOffering", mock.Anything, mock.Anything).Return(out, apiErr)
		rec2 := &optRecordingElastiCache{MockElastiCacheClient: m}
		res, err := (&Client{client: rec2, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
		return rec2.attempts, res, err
	}
	purchasetest.RunEmptyToken(t, call, &types.ReservedCacheNodeAlreadyExistsFault{})
}
