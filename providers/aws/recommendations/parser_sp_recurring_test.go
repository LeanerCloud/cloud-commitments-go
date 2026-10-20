package recommendations

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Values recorded from a live EC2_INSTANCE_SP / ONE_YEAR / PARTIAL_UPFRONT
// CE response (m5, eu-west-2): hourly 2.760, upfront 12089.
func TestParseSavingsPlanDetail_RecurringMonthlyCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, term, payment, hourly, upfront string
		want                                 float64
	}{
		{"partial upfront 1yr (live CE values)", "1yr", "partial-upfront", "2.760", "12089.0000000000", 1007.3833},
		{"partial upfront 3yr", "3yr", "partial-upfront", "1.000", "13140", 365.0},
		{"no upfront is hourly x 730", "1yr", "no-upfront", "2.760", "0", 2014.8},
		{"all upfront is an explicit zero", "1yr", "all-upfront", "2.760", "24177", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := (&Client{}).parseSavingsPlanDetail(&types.SavingsPlansPurchaseRecommendationDetail{
				HourlyCommitmentToPurchase: aws.String(tt.hourly),
				UpfrontCost:                aws.String(tt.upfront),
			}, &common.RecommendationParams{Term: tt.term, PaymentOption: tt.payment}, types.SupportedSavingsPlansTypeEc2InstanceSp)
			require.NoError(t, err)
			require.NotNil(t, rec.RecurringMonthlyCost)
			assert.InDelta(t, tt.want, *rec.RecurringMonthlyCost, 0.001)
		})
	}
}

func TestParseSavingsPlanDetail_RecurringMonthlyCostErrors(t *testing.T) {
	t.Parallel()
	detail := &types.SavingsPlansPurchaseRecommendationDetail{
		HourlyCommitmentToPurchase: aws.String("1.0"),
		UpfrontCost:                aws.String("99999"),
	}
	_, err := (&Client{}).parseSavingsPlanDetail(detail, &common.RecommendationParams{Term: "1yr", PaymentOption: "partial-upfront"}, types.SupportedSavingsPlansTypeComputeSp)
	require.ErrorContains(t, err, "exceeds")
	_, err = (&Client{}).parseSavingsPlanDetail(detail, &common.RecommendationParams{Term: "2yr", PaymentOption: "no-upfront"}, types.SupportedSavingsPlansTypeComputeSp)
	require.ErrorContains(t, err, "unsupported term")
}
