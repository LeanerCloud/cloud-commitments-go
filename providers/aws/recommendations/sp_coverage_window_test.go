package recommendations

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSPCoverageSummary_SparseWindowRates(t *testing.T) {
	for _, mode := range []string{"omitted", "zero", "nil"} {
		for _, paged := range []bool{false, true} {
			t.Run(mode+"/paged="+strconv.FormatBool(paged), func(t *testing.T) {
				periods := make([]types.SavingsPlansCoverage, 0, 30)
				start := time.Now().UTC().AddDate(0, 0, -30)
				for day := 0; day < 30; day++ {
					if day >= 10 && mode == "omitted" {
						continue
					}
					cost := "12"
					if day >= 10 {
						cost = "0"
					}
					period := types.SavingsPlansCoverage{
						TimePeriod: &types.DateInterval{Start: aws.String(start.AddDate(0, 0, day).Format(time.DateOnly)), End: aws.String(start.AddDate(0, 0, day+1).Format(time.DateOnly))},
						Coverage:   &types.SavingsPlansCoverageData{SpendCoveredBySavingsPlans: aws.String(cost), OnDemandCost: aws.String(cost)},
					}
					if day >= 10 && mode == "nil" {
						period.Coverage = nil
					}
					periods = append(periods, period)
				}
				pages := []*costexplorer.GetSavingsPlansCoverageOutput{{SavingsPlansCoverages: periods}}
				if paged {
					pages = []*costexplorer.GetSavingsPlansCoverageOutput{
						{SavingsPlansCoverages: periods[:5], NextToken: aws.String("second")},
						{SavingsPlansCoverages: periods[5:]},
					}
				}
				mock := &mockSPCE{coveragePages: pages}
				got, err := NewClientWithAPI(mock, "us-east-1").GetSPCoverageSummary(context.Background(), "us-east-1", 30)
				require.NoError(t, err)
				require.NotNil(t, got.CoveredUSDPerHour)
				require.NotNil(t, got.OnDemandUSDPerHour)
				require.NotNil(t, got.EligibleUSDPerHour)
				require.NotNil(t, got.CoveragePct)
				assert.InDelta(t, 1.0/6, *got.CoveredUSDPerHour, 1e-9)
				assert.InDelta(t, 1.0/6, *got.OnDemandUSDPerHour, 1e-9)
				assert.InDelta(t, 1.0/3, *got.EligibleUSDPerHour, 1e-9)
				assert.InDelta(t, 50, *got.CoveragePct, 1e-9)
				wantDays := 10
				if mode == "zero" {
					wantDays = 30
				}
				assert.Equal(t, wantDays, got.Days)
				assert.Len(t, mock.coverageInputs, len(pages))
				if paged {
					assert.Equal(t, "second", aws.ToString(mock.coverageInputs[1].NextToken))
				}
			})
		}
	}
}
