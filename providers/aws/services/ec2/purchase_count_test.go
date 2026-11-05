package ec2

import (
	"context"
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPurchaseCommitment_Count(t *testing.T) {
	t.Parallel()
	rec := func(n int) common.Recommendation {
		return common.Recommendation{ResourceType: "t3.micro", Count: n, PaymentOption: "partial-upfront", Term: "3yr",
			Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"}}
	}

	t.Run("max int32 is sent exactly", func(t *testing.T) {
		t.Parallel()
		m := &MockEC2Client{}
		m.Test(t)
		m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
			Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
				ReservedInstancesOfferingId: aws.String("offering-123"), InstanceType: types.InstanceTypeT3Micro,
				Duration: aws.Int64(94608000), OfferingType: types.OfferingTypeValuesPartialUpfront,
				ProductDescription: types.RIProductDescriptionLinuxUnix, InstanceTenancy: types.TenancyDefault, OfferingClass: types.OfferingClassTypeConvertible,
			}}}, nil)
		m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.MatchedBy(func(in *ec2.PurchaseReservedInstancesOfferingInput) bool {
			return aws.ToInt32(in.InstanceCount) == math.MaxInt32
		})).Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-1")}, nil).Once()
		m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil)

		result, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec(math.MaxInt32), common.PurchaseOptions{})
		require.NoError(t, err)
		assert.True(t, result.Success)
		m.AssertNumberOfCalls(t, "PurchaseReservedInstancesOffering", 1)
	})

	for _, n := range []int{math.MaxInt32 + 1, 0, -1} {
		t.Run("refused before any API call", func(t *testing.T) {
			t.Parallel()
			m := &MockEC2Client{}
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
