package rds

import (
	"context"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPurchaseCommitment_Count(t *testing.T) {
	t.Parallel()
	rec := func(n int) common.Recommendation {
		return common.Recommendation{
			Service: common.ServiceRelationalDB, ResourceType: "db.t3.micro", Count: n,
			PaymentOption: "all-upfront", Term: "1yr",
			Details: &common.DatabaseDetails{Engine: "mysql", AZConfig: "single-az"},
		}
	}

	t.Run("max int32 is sent exactly", func(t *testing.T) {
		t.Parallel()
		m := &MockRDSClient{}
		m.Test(t)
		m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
			Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
				ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{{
					ReservedDBInstancesOfferingId: aws.String("offering-123"),
					DBInstanceClass:               aws.String("db.t3.micro"),
					ProductDescription:            aws.String("mysql"),
					MultiAZ:                       aws.Bool(false),
					OfferingType:                  aws.String("All Upfront"),
					Duration:                      aws.Int32(31536000),
				}},
			}, nil)
		m.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.MatchedBy(func(in *rds.PurchaseReservedDBInstancesOfferingInput) bool {
			return aws.ToInt32(in.DBInstanceCount) == math.MaxInt32
		})).Return(&rds.PurchaseReservedDBInstancesOfferingOutput{ReservedDBInstance: &types.ReservedDBInstance{ReservedDBInstanceId: aws.String("ri-1")}}, nil).Once()

		result, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec(math.MaxInt32), common.PurchaseOptions{})
		require.NoError(t, err)
		assert.True(t, result.Success)
		m.AssertNumberOfCalls(t, "PurchaseReservedDBInstancesOffering", 1)
	})

	for _, n := range []int{math.MaxInt32 + 1, 0, -1} {
		t.Run("refused before any API call", func(t *testing.T) {
			t.Parallel()
			m := &MockRDSClient{}
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
