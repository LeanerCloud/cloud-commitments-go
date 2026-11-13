package recommendations

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// slowRIAPI delays every Reservation call so the Savings Plans task finishes
// first, the reverse of the canonical merge order.
type slowRIAPI struct {
	*mockCostExplorerAPI
	delay time.Duration
}

func (s *slowRIAPI) GetReservationPurchaseRecommendation(ctx context.Context, in *costexplorer.GetReservationPurchaseRecommendationInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetReservationPurchaseRecommendationOutput, error) {
	time.Sleep(s.delay)
	return s.mockCostExplorerAPI.GetReservationPurchaseRecommendation(ctx, in, optFns...)
}

func ec2ReservationOutput() *costexplorer.GetReservationPurchaseRecommendationOutput {
	return &costexplorer.GetReservationPurchaseRecommendationOutput{
		Recommendations: []types.ReservationPurchaseRecommendation{{
			RecommendationDetails: []types.ReservationPurchaseRecommendationDetail{{
				RecommendedNumberOfInstancesToPurchase: aws.String("1"),
				EstimatedMonthlySavingsAmount:          aws.String("50.00"),
				EstimatedMonthlySavingsPercentage:      aws.String("20.0"),
				InstanceDetails: &types.InstanceDetails{EC2InstanceDetails: &types.EC2InstanceDetails{
					InstanceType: aws.String("t3.medium"),
					Platform:     aws.String("Linux/UNIX"),
					Region:       aws.String("us-east-1"),
				}},
			}},
		}},
	}
}

func computeSPOutput() *costexplorer.GetSavingsPlansPurchaseRecommendationOutput {
	return &costexplorer.GetSavingsPlansPurchaseRecommendationOutput{
		SavingsPlansPurchaseRecommendation: &types.SavingsPlansPurchaseRecommendation{
			SavingsPlansPurchaseRecommendationDetails: []types.SavingsPlansPurchaseRecommendationDetail{{
				HourlyCommitmentToPurchase:    aws.String("1.00"),
				EstimatedMonthlySavingsAmount: aws.String("120.00"),
				EstimatedSavingsPercentage:    aws.String("30.0"),
				UpfrontCost:                   aws.String("0"),
				AccountId:                     aws.String("123456789012"),
			}},
		},
	}
}

func TestGetAllRecommendations_MergeOrderIgnoresCompletionOrder(t *testing.T) {
	t.Parallel()
	api := &slowRIAPI{
		mockCostExplorerAPI: &mockCostExplorerAPI{riRecommendations: ec2ReservationOutput(), spRecommendations: computeSPOutput()},
		delay:               40 * time.Millisecond,
	}
	recs, err := NewClientWithAPI(api, "us-east-1").GetAllRecommendations(context.Background())
	var incomplete *IncompleteRecommendationsError
	require.ErrorAs(t, err, &incomplete) // the shared EC2 fixture fails to parse for the other RI services
	require.NotEmpty(t, recs)
	assert.Equal(t, common.ServiceEC2, recs[0].Service, "EC2 merges first although it completes last")
	assert.True(t, common.IsSavingsPlan(recs[len(recs)-1].Service), "Savings Plans merge last although they complete first")
}

func TestGetAllRecommendations_MidFanOutDeadlineReturnsCtxErrNotPartialData(t *testing.T) {
	t.Parallel()
	api := &slowRIAPI{
		mockCostExplorerAPI: &mockCostExplorerAPI{riRecommendations: ec2ReservationOutput(), spRecommendations: computeSPOutput()},
		delay:               40 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	recs, err := NewClientWithAPI(api, "us-east-1").GetAllRecommendations(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, recs)
}
