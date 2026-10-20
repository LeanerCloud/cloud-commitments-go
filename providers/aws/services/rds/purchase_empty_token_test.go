package rds

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg/purchasetest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/mock"
)

// optRecordingRDS records the RetryMaxAttempts the per-operation options set.
type optRecordingRDS struct {
	*MockRDSClient
	attempts int
}

func (r *optRecordingRDS) PurchaseReservedDBInstancesOffering(ctx context.Context, p *rds.PurchaseReservedDBInstancesOfferingInput, optFns ...func(*rds.Options)) (*rds.PurchaseReservedDBInstancesOfferingOutput, error) {
	var o rds.Options
	for _, f := range optFns {
		f(&o)
	}
	r.attempts = o.RetryMaxAttempts
	return r.MockRDSClient.PurchaseReservedDBInstancesOffering(ctx, p, optFns...)
}

func TestPurchaseCommitment_EmptyToken(t *testing.T) {
	t.Parallel()
	call := func(t *testing.T, token string, apiErr error, emptyResp bool) (int, common.PurchaseResult, error) {
		m := &MockRDSClient{}
		rec := common.Recommendation{
			Service: common.ServiceRelationalDB, ResourceType: "db.t3.micro", Count: 1,
			PaymentOption: "all-upfront", Term: "1yr",
			Details: &common.DatabaseDetails{Engine: "mysql", AZConfig: "single-az"},
		}
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
		m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
			Return(&rds.DescribeReservedDBInstancesOutput{}, nil)
		out := purchasetest.Pick(&rds.PurchaseReservedDBInstancesOfferingOutput{ReservedDBInstance: &types.ReservedDBInstance{ReservedDBInstanceId: aws.String("ri-1")}},
			&rds.PurchaseReservedDBInstancesOfferingOutput{}, apiErr, emptyResp)
		m.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).Return(out, apiErr)
		rec2 := &optRecordingRDS{MockRDSClient: m}
		res, err := (&Client{client: rec2, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
		return rec2.attempts, res, err
	}
	purchasetest.RunEmptyToken(t, call, &types.ReservedDBInstanceAlreadyExistsFault{})
}
