package opensearch

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg/purchasetest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"
	"github.com/stretchr/testify/mock"
)

// optRecordingOpenSearch records the RetryMaxAttempts the per-operation options set.
type optRecordingOpenSearch struct {
	*MockOpenSearchClient
	attempts int
}

func (r *optRecordingOpenSearch) PurchaseReservedInstanceOffering(ctx context.Context, p *opensearch.PurchaseReservedInstanceOfferingInput, optFns ...func(*opensearch.Options)) (*opensearch.PurchaseReservedInstanceOfferingOutput, error) {
	var o opensearch.Options
	for _, f := range optFns {
		f(&o)
	}
	r.attempts = o.RetryMaxAttempts
	return r.MockOpenSearchClient.PurchaseReservedInstanceOffering(ctx, p, optFns...)
}

func TestPurchaseCommitment_EmptyToken(t *testing.T) {
	call := func(t *testing.T, token string, apiErr error, emptyResp bool) (int, common.PurchaseResult, error) {
		m := &MockOpenSearchClient{}
		rec := common.Recommendation{
			Service: common.ServiceSearch, ResourceType: "m5.large.search", Count: 1,
			PaymentOption: "partial-upfront", Term: "1yr",
			Details: common.SearchDetails{InstanceType: "m5.large.search"},
		}
		m.On("DescribeReservedInstanceOfferings", mock.Anything, mock.Anything).
			Return(&opensearch.DescribeReservedInstanceOfferingsOutput{
				ReservedInstanceOfferings: []types.ReservedInstanceOffering{{
					ReservedInstanceOfferingId: aws.String("offering-123"),
					InstanceType:               types.OpenSearchPartitionInstanceTypeM5LargeSearch,
					Duration:                   31536000,
					PaymentOption:              types.ReservedInstancePaymentOptionPartialUpfront,
				}},
			}, nil)
		m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
			Return(&opensearch.DescribeReservedInstancesOutput{}, nil)
		m.On("AddTags", mock.Anything, mock.Anything).Return(&opensearch.AddTagsOutput{}, nil).Maybe()
		out := purchasetest.Pick(&opensearch.PurchaseReservedInstanceOfferingOutput{ReservedInstanceId: aws.String("ri-1")},
			&opensearch.PurchaseReservedInstanceOfferingOutput{}, apiErr, emptyResp)
		m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).Return(out, apiErr)
		rec2 := &optRecordingOpenSearch{MockOpenSearchClient: m}
		res, err := (&Client{client: rec2, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
		return rec2.attempts, res, err
	}
	purchasetest.RunEmptyToken(t, call, &types.ResourceAlreadyExistsException{})
}
