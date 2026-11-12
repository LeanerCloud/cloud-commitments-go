package recommendations

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// pricedCases covers every combination of the two source figures Priced
// depends on (on-demand baseline, savings) for go#326.
var pricedCases = []struct {
	name                 string
	hasOnDemand, hasSave bool
	want                 bool
}{
	{"both present", true, true, true},
	{"on-demand absent", false, true, false},
	{"savings absent", true, false, false},
	{"both absent", false, false, false},
}

func TestParseRecommendationDetail_Priced(t *testing.T) {
	t.Parallel()
	for _, tc := range pricedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			details := &types.ReservationPurchaseRecommendationDetail{
				RecommendedNumberOfInstancesToPurchase: aws.String("2"),
				InstanceDetails: &types.InstanceDetails{
					EC2InstanceDetails: &types.EC2InstanceDetails{
						InstanceType: aws.String("m5.large"),
						Platform:     aws.String("Linux/UNIX"),
						Region:       aws.String("us-east-1"),
						Tenancy:      aws.String("shared"),
					},
				},
			}
			if tc.hasOnDemand {
				details.EstimatedMonthlyOnDemandCost = aws.String("0")
			}
			if tc.hasSave {
				details.EstimatedMonthlySavingsAmount = aws.String("0")
			}
			params := common.RecommendationParams{Service: common.ServiceEC2, PaymentOption: "no-upfront", Term: "1yr", LookbackPeriod: "7d"}

			rec, err := (&Client{}).parseRecommendationDetail(context.Background(), details, params)
			require.NoError(t, err)
			require.NotNil(t, rec)
			assert.Equal(t, tc.want, rec.Priced)
		})
	}
}

func TestParseSavingsPlanDetail_Priced(t *testing.T) {
	t.Parallel()
	for _, tc := range pricedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			detail := &types.SavingsPlansPurchaseRecommendationDetail{
				HourlyCommitmentToPurchase: aws.String("2.50"),
				UpfrontCost:                aws.String("0"),
			}
			if tc.hasOnDemand {
				detail.CurrentAverageHourlyOnDemandSpend = aws.String("0")
			}
			if tc.hasSave {
				detail.EstimatedMonthlySavingsAmount = aws.String("0")
			}
			params := common.RecommendationParams{PaymentOption: "no-upfront", Term: "1yr", LookbackPeriod: "7d"}

			rec, err := (&Client{}).parseSavingsPlanDetail(detail, &params, types.SupportedSavingsPlansTypeComputeSp)
			require.NoError(t, err)
			require.NotNil(t, rec)
			assert.Equal(t, tc.want, rec.Priced)
		})
	}
}
