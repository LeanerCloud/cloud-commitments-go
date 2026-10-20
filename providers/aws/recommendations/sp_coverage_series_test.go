package recommendations

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covDay(date, spend string) types.SavingsPlansCoverage {
	c := types.SavingsPlansCoverage{TimePeriod: &types.DateInterval{Start: aws.String(date), End: aws.String(date)}}
	if spend != "" {
		c.Coverage = &types.SavingsPlansCoverageData{SpendCoveredBySavingsPlans: aws.String(spend)}
	}
	return c
}

func TestGetSPCoveredSeries(t *testing.T) {
	t.Parallel()
	page1 := &costexplorer.GetSavingsPlansCoverageOutput{
		SavingsPlansCoverages: []types.SavingsPlansCoverage{
			covDay("2026-01-02", "240"), // 240/24 = 10 OD$/hr
			covDay("2026-01-01", "120"),
		},
		NextToken: aws.String("p2"),
	}
	page2 := &costexplorer.GetSavingsPlansCoverageOutput{
		SavingsPlansCoverages: []types.SavingsPlansCoverage{
			covDay("2026-01-03", ""), // nil Coverage block counts as zero
		},
	}
	mock := &mockSPCE{coveragePages: []*costexplorer.GetSavingsPlansCoverageOutput{page1, page2}}
	got, err := NewClientWithAPI(mock, "us-east-1").GetSPCoveredSeries(context.Background(), "us-east-1", 30)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "2026-01-01", got[0].Date.Format(ceDateLayout), "sorted oldest-to-newest")
	assert.InDelta(t, 5.0, got[0].USDPerHour, 1e-9)
	assert.InDelta(t, 10.0, got[1].USDPerHour, 1e-9)
	assert.Zero(t, got[2].USDPerHour)
	require.Len(t, mock.coverageInputs, 2)
	assert.Equal(t, types.GranularityDaily, mock.coverageInputs[0].Granularity)
	assert.Equal(t, []string{spCoverageMetricSpendCovered}, mock.coverageInputs[0].Metrics)
}

// TestGetSPCoveredSeries_Filter pins the SERVICE and REGION clauses. The
// SERVICE string is unverified against live CE (see spCoverageServiceEC2);
// this test asserts the clause exists and carries the named constant, so
// dropping it (which would pull Fargate/Lambda into EC2 demand and over-buy)
// fails here.
func TestGetSPCoveredSeries_Filter(t *testing.T) {
	t.Parallel()
	mock := &mockSPCE{}
	_, err := NewClientWithAPI(mock, "us-east-1").GetSPCoveredSeries(context.Background(), "eu-west-2", 30)
	require.NoError(t, err)
	require.Len(t, mock.coverageInputs, 1)
	f := mock.coverageInputs[0].Filter
	require.NotNil(t, f)
	require.Len(t, f.And, 2)
	got := map[types.Dimension][]string{}
	for _, e := range f.And {
		require.NotNil(t, e.Dimensions)
		got[e.Dimensions.Key] = e.Dimensions.Values
	}
	assert.Equal(t, []string{spCoverageServiceEC2}, got[types.DimensionService])
	assert.Equal(t, []string{"eu-west-2"}, got[types.DimensionRegion])
	assert.NotContains(t, got, types.DimensionSavingsPlansType, "coverage rejects SAVINGS_PLANS_TYPE")
}

func TestGetSPCoveredSeries_EmptyIsNotAnError(t *testing.T) {
	t.Parallel()
	got, err := NewClientWithAPI(&mockSPCE{}, "us-east-1").GetSPCoveredSeries(context.Background(), "us-east-1", 30)
	require.NoError(t, err)
	assert.Empty(t, got, "an account without SPs has no covered usage")
}

func TestGetSPCoveredSeries_FailLoud(t *testing.T) {
	t.Parallel()
	undated := types.SavingsPlansCoverage{Coverage: &types.SavingsPlansCoverageData{SpendCoveredBySavingsPlans: aws.String("1")}}
	noMetric := covDay("2026-01-01", "")
	noMetric.Coverage = &types.SavingsPlansCoverageData{}
	tests := []struct {
		name string
		rows []types.SavingsPlansCoverage
	}{
		{"undated row", []types.SavingsPlansCoverage{undated}},
		{"missing metric in non-nil block", []types.SavingsPlansCoverage{noMetric}},
		{"unparseable", []types.SavingsPlansCoverage{covDay("2026-01-01", "abc")}},
		{"negative", []types.SavingsPlansCoverage{covDay("2026-01-01", "-5")}},
		{"duplicate day", []types.SavingsPlansCoverage{covDay("2026-01-01", "1"), covDay("2026-01-01", "2")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSPCE{coveragePages: []*costexplorer.GetSavingsPlansCoverageOutput{{SavingsPlansCoverages: tc.rows}}}
			_, err := NewClientWithAPI(mock, "us-east-1").GetSPCoveredSeries(context.Background(), "us-east-1", 30)
			require.Error(t, err)
		})
	}
}

func TestGetSPCoveredSeries_InvalidArgs(t *testing.T) {
	t.Parallel()
	c := NewClientWithAPI(&mockSPCE{}, "us-east-1")
	_, err := c.GetSPCoveredSeries(context.Background(), "", 30)
	require.Error(t, err)
	_, err = c.GetSPCoveredSeries(context.Background(), "us-east-1", 0)
	require.Error(t, err)
}

func TestSPUtilization_OnDemandEquivalentAndRatio(t *testing.T) {
	t.Parallel()
	// Window totals over 30 days (720h): used 3024 => 4.2/hr, on-demand
	// equivalent 6000 => 8.3333/hr; k = 0.504.
	out := &costexplorer.GetSavingsPlansUtilizationOutput{Total: &types.SavingsPlansUtilizationAggregates{
		Utilization: &types.SavingsPlansUtilization{UsedCommitment: aws.String("3024"), TotalCommitment: aws.String("4320")},
		Savings:     &types.SavingsPlansSavings{OnDemandCostEquivalent: aws.String("6000")},
	}}
	s, err := NewClientWithAPI(&mockSPCE{utilOutput: out}, "us-east-1").
		GetSPUtilization(context.Background(), types.SupportedSavingsPlansTypeComputeSp, "", 30)
	require.NoError(t, err)
	require.NotNil(t, s.OnDemandCostEquivalentUSDPerHour)
	assert.InDelta(t, 6000.0/720, *s.OnDemandCostEquivalentUSDPerHour, 1e-9)
	k, err := s.CommitmentToOnDemandRatio()
	require.NoError(t, err)
	assert.InDelta(t, 0.504, k, 1e-9)
}

func TestCommitmentToOnDemandRatio_Errors(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	_, e0 := SPUtilizationSummary{}.CommitmentToOnDemandRatio()
	assert.ErrorIs(t, e0, ErrNoSPUtilization)
	for name, s := range map[string]SPUtilizationSummary{
		"zero used": {UsedCommitmentUSDPerHour: f(0), OnDemandCostEquivalentUSDPerHour: f(5)},
		"zero od":   {UsedCommitmentUSDPerHour: f(5), OnDemandCostEquivalentUSDPerHour: f(0)},
		"nil od":    {UsedCommitmentUSDPerHour: f(5)},
	} {
		_, err := s.CommitmentToOnDemandRatio()
		assert.ErrorIs(t, err, ErrNoSPUtilization, name)
	}
	_, err := SPUtilizationSummary{UsedCommitmentUSDPerHour: f(6), OnDemandCostEquivalentUSDPerHour: f(5)}.CommitmentToOnDemandRatio()
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoSPUtilization, "k > 1 is a data error, not a missing-data Hold")
	k, err := SPUtilizationSummary{UsedCommitmentUSDPerHour: f(5), OnDemandCostEquivalentUSDPerHour: f(5)}.CommitmentToOnDemandRatio()
	require.NoError(t, err)
	assert.InDelta(t, 1.0, k, 1e-12, "k == 1 (no discount) is valid")
}
