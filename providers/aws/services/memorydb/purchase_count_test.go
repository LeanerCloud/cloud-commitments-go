package memorydb

import (
	"context"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	"github.com/aws/aws-sdk-go-v2/service/memorydb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPurchaseCommitment_Count(t *testing.T) {
	t.Parallel()
	rec := func(n int) common.Recommendation {
		r := mdbIdemRec()
		r.Count = n
		return r
	}

	t.Run("max int32 is sent exactly", func(t *testing.T) {
		t.Parallel()
		m := &MockMemoryDBClient{}
		m.Test(t)
		expectMDBOffering(m)
		m.On("PurchaseReservedNodesOffering", mock.Anything, mock.MatchedBy(func(in *memorydb.PurchaseReservedNodesOfferingInput) bool {
			return aws.ToInt32(in.NodeCount) == math.MaxInt32
		})).Return(&memorydb.PurchaseReservedNodesOfferingOutput{ReservedNode: &types.ReservedNode{ReservationId: aws.String("rn-1")}}, nil).Once()

		result, err := (&Client{client: m, region: "eu-west-1"}).PurchaseCommitment(context.Background(), rec(math.MaxInt32), common.PurchaseOptions{})
		require.NoError(t, err)
		assert.True(t, result.Success)
		m.AssertNumberOfCalls(t, "PurchaseReservedNodesOffering", 1)
	})

	for _, n := range []int{math.MaxInt32 + 1, 0, -1} {
		t.Run("refused before any API call", func(t *testing.T) {
			t.Parallel()
			m := &MockMemoryDBClient{}
			m.Test(t)
			opts := common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-count", 0)}

			result, err := (&Client{client: m, region: "eu-west-1"}).PurchaseCommitment(context.Background(), rec(n), opts)
			require.Error(t, err)
			assert.False(t, result.Success)
			assert.ErrorIs(t, err, offeringprice.ErrPurchaseCountOutOfRange)
			assert.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
			assert.Empty(t, m.Calls, "a bad count must make no describe and no purchase call")
		})
	}
}
