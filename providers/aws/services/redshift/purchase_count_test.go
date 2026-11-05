package redshift

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPurchaseCommitment_Count(t *testing.T) {
	t.Parallel()
	rec := func(n int) common.Recommendation {
		return common.Recommendation{ResourceType: "dc2.large", Count: n, PaymentOption: "all-upfront", Term: "1yr",
			Details: common.DataWarehouseDetails{NodeType: "dc2.large", NumberOfNodes: 2}}
	}

	t.Run("max int32 is sent exactly", func(t *testing.T) {
		t.Parallel()
		m := &MockRedshiftClient{}
		m.Test(t)
		m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
			Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: []types.ReservedNodeOffering{{
				ReservedNodeOfferingId: aws.String("offering-123"), NodeType: aws.String("dc2.large"),
				Duration: aws.Int32(31536000), ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"),
				FixedPrice: aws.Float64(500),
			}}}, nil)
		m.On("PurchaseReservedNodeOffering", mock.Anything, mock.MatchedBy(func(in *redshift.PurchaseReservedNodeOfferingInput) bool {
			return aws.ToInt32(in.NodeCount) == math.MaxInt32
		})).Return(&redshift.PurchaseReservedNodeOfferingOutput{ReservedNode: &types.ReservedNode{ReservedNodeId: aws.String("rn-1")}}, nil).Once()

		result, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec(math.MaxInt32), common.PurchaseOptions{})
		require.NoError(t, err)
		assert.True(t, result.Success)
		m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
	})

	for _, n := range []int{math.MaxInt32 + 1, 0, -1} {
		t.Run(fmt.Sprintf("refused before any API call, count %d", n), func(t *testing.T) {
			t.Parallel()
			m := &MockRedshiftClient{}
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
