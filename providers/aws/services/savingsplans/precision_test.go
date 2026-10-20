package savingsplans

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestCommitmentPrecision_PurchaseAndPreview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		amount float64
		wire   string
	}{
		{.001, "0.001"}, {.00499, "0.00499"}, {.005, "0.005"},
		{1.234567, "1.23456"}, {.29, "0.29"}, {10, "10.00"},
		{1000000, "1000000.00"}, {math.Nextafter(.001, math.Inf(1)), "0.001"},
		{math.Nextafter(1000000, 0), "999999.99999"},
		{math.Nextafter(.29, 0), "0.28999"}, {math.Nextafter(.29, math.Inf(1)), "0.29"},
	} {
		t.Run(strconv.FormatFloat(tc.amount, 'g', -1, 64), func(t *testing.T) {
			t.Parallel()
			m := &MockSavingsPlansClient{}
			c := NewClient(aws.Config{Region: "us-east-1"}, types.SavingsPlanTypeCompute)
			c.SetSavingsPlansAPI(m)
			rec := spRec()
			original := rec.Details.(*common.SavingsPlanDetails)
			original.HourlyCommitment = tc.amount
			original.OfferingID = "offering"
			want, err := strconv.ParseFloat(tc.wire, 64)
			require.NoError(t, err)
			m.On("DescribeSavingsPlansOfferingRates", mock.Anything, mock.Anything).
				Return(&savingsplans.DescribeSavingsPlansOfferingRatesOutput{}, nil).Once()
			preview, err := c.GetOfferingDetails(context.Background(), rec)
			require.NoError(t, err)
			assert.Equal(t, want, preview.EffectiveHourlyRate)
			assert.Equal(t, want*8760, preview.TotalCost)
			assert.Equal(t, (want*8760)/8760, preview.RecurringCost)
			m.On("CreateSavingsPlan", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				input := args.Get(1).(*savingsplans.CreateSavingsPlanInput)
				assert.Equal(t, tc.wire, aws.ToString(input.Commitment))
				assert.Equal(t, "stable-token", aws.ToString(input.ClientToken))
			}).Return(&savingsplans.CreateSavingsPlanOutput{SavingsPlanId: aws.String("purchased")}, nil).Twice()
			m.On("DescribeSavingsPlans", mock.Anything, mock.Anything).
				Return(&savingsplans.DescribeSavingsPlansOutput{SavingsPlans: []types.SavingsPlan{{SavingsPlanId: aws.String("purchased"), UpfrontPaymentAmount: aws.String("123.45")}}}, nil).Twice()
			for i := 0; i < 2; i++ {
				result, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: "stable-token"})
				require.NoError(t, err)
				require.True(t, result.Success)
				normalized := result.Recommendation.Details.(*common.SavingsPlanDetails)
				assert.Equal(t, want, normalized.HourlyCommitment)
				assert.NotSame(t, original, normalized)
				require.NotNil(t, result.Cost)
				assert.Equal(t, 123.45, *result.Cost)
				rec = result.Recommendation
			}
			assert.Equal(t, tc.amount, original.HourlyCommitment)
			m.AssertExpectations(t)
		})
	}
}

func TestCommitmentPrecision_InvalidInputMakesNoSDKCalls(t *testing.T) {
	t.Parallel()
	amounts := []float64{0, -1, .00099, math.Nextafter(.001, 0), math.Nextafter(1000000, math.Inf(1)), 1000001, math.NaN(), math.Inf(1), math.Inf(-1)}
	inputs := make([]common.ServiceDetails, 0, len(amounts)+2)
	inputs = append(inputs, nil, (*common.SavingsPlanDetails)(nil))
	for _, amount := range amounts {
		inputs = append(inputs, &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: amount})
	}
	for i, details := range inputs {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			m := &MockSavingsPlansClient{}
			c := NewClient(aws.Config{}, types.SavingsPlanTypeCompute)
			c.SetSavingsPlansAPI(m)
			rec := spRec()
			rec.Details = details
			require.NotPanics(t, func() {
				result, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
				require.Error(t, err)
				assert.False(t, result.Success)
				_, err = c.GetOfferingDetails(context.Background(), rec)
				require.Error(t, err)
			})
			assert.Empty(t, m.Calls)
		})
	}
}

func TestCommitmentPrecision_ValidateOfferingDoesNotRequireCommitment(t *testing.T) {
	t.Parallel()
	c := NewClient(aws.Config{}, types.SavingsPlanTypeCompute)
	m := &MockSavingsPlansClient{}
	c.SetSavingsPlansAPI(m)
	rec := spRec()
	rec.Details = (*common.SavingsPlanDetails)(nil)
	require.NotPanics(t, func() { require.Error(t, c.ValidateOffering(context.Background(), rec)) })
	rec.Details = &common.SavingsPlanDetails{PlanType: "Compute", OfferingID: "offering"}
	require.NoError(t, c.ValidateOffering(context.Background(), rec))
	assert.Empty(t, m.Calls)
}
