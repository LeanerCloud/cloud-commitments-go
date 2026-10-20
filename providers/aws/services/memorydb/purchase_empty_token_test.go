package memorydb

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg/purchasetest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	"github.com/aws/aws-sdk-go-v2/service/memorydb/types"
	"github.com/stretchr/testify/mock"
)

// optRecordingMemoryDB records the RetryMaxAttempts the per-operation options set.
type optRecordingMemoryDB struct {
	*MockMemoryDBClient
	attempts int
}

func (r *optRecordingMemoryDB) PurchaseReservedNodesOffering(ctx context.Context, p *memorydb.PurchaseReservedNodesOfferingInput, optFns ...func(*memorydb.Options)) (*memorydb.PurchaseReservedNodesOfferingOutput, error) {
	var o memorydb.Options
	for _, f := range optFns {
		f(&o)
	}
	r.attempts = o.RetryMaxAttempts
	return r.MockMemoryDBClient.PurchaseReservedNodesOffering(ctx, p, optFns...)
}

func TestPurchaseCommitment_EmptyToken(t *testing.T) {
	t.Parallel()
	call := func(t *testing.T, token string, apiErr error, emptyResp bool) (int, common.PurchaseResult, error) {
		m := &MockMemoryDBClient{}
		rec := common.Recommendation{
			Service: common.ServiceCache, ResourceType: "db.r6gd.xlarge", Count: 1,
			PaymentOption: "partial-upfront", Term: "1yr",
			Details: &common.CacheDetails{Engine: "redis", NodeType: "db.r6gd.xlarge"},
		}
		m.On("DescribeReservedNodesOfferings", mock.Anything, mock.Anything).
			Return(&memorydb.DescribeReservedNodesOfferingsOutput{
				ReservedNodesOfferings: []types.ReservedNodesOffering{{
					ReservedNodesOfferingId: aws.String("offering-123"),
					NodeType:                aws.String("db.r6gd.xlarge"),
					Duration:                31536000,
					OfferingType:            aws.String("Partial Upfront"),
				}},
			}, nil)
		m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
			Return(&memorydb.DescribeReservedNodesOutput{}, nil)
		out := purchasetest.Pick(&memorydb.PurchaseReservedNodesOfferingOutput{ReservedNode: &types.ReservedNode{ReservationId: aws.String("rn-1")}},
			&memorydb.PurchaseReservedNodesOfferingOutput{}, apiErr, emptyResp)
		m.On("PurchaseReservedNodesOffering", mock.Anything, mock.Anything).Return(out, apiErr)
		rec2 := &optRecordingMemoryDB{MockMemoryDBClient: m}
		res, err := (&Client{client: rec2, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
		return rec2.attempts, res, err
	}
	purchasetest.RunEmptyToken(t, call, &types.ReservedNodeAlreadyExistsFault{})
}
