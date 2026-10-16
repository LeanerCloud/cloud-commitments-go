package recommendations

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaginateCE_FollowsTokens(t *testing.T) {
	var seen []*string
	err := paginateCE(context.Background(), 10, "x", func(token *string) (*string, error) {
		seen = append(seen, token)
		if len(seen) == 3 {
			return nil, nil
		}
		return aws.String(fmt.Sprintf("t%d", len(seen))), nil
	})
	require.NoError(t, err)
	require.Len(t, seen, 3)
	assert.Nil(t, seen[0])
	assert.Equal(t, "t1", *seen[1])
	assert.Equal(t, "t2", *seen[2])
}

func TestPaginateCE_EmptyTokenEndsLoop(t *testing.T) {
	calls := 0
	err := paginateCE(context.Background(), 10, "x", func(*string) (*string, error) {
		calls++
		return aws.String(""), nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestPaginateCE_CapErrorsAfterExactlyMaxPages(t *testing.T) {
	calls := 0
	err := paginateCE(context.Background(), 5, "thing", func(*string) (*string, error) {
		calls++
		return aws.String("more"), nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Contains(t, err.Error(), "thing: ")
	assert.Equal(t, 5, calls)
}

func TestPaginateCE_ExactlyMaxPagesSucceeds(t *testing.T) {
	calls := 0
	err := paginateCE(context.Background(), 3, "x", func(*string) (*string, error) {
		calls++
		if calls == 3 {
			return nil, nil
		}
		return aws.String("more"), nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestPaginateCE_CanceledBeforeFirstCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := paginateCE(ctx, 5, "x", func(*string) (*string, error) {
		calls++
		return nil, nil
	})
	assert.Equal(t, context.Canceled, err, "ctx error must be returned raw")
	assert.Zero(t, calls)
}

func TestPaginateCE_CanceledMidLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := paginateCE(ctx, 5, "x", func(*string) (*string, error) {
		calls++
		cancel()
		return aws.String("more"), nil
	})
	assert.Equal(t, context.Canceled, err)
	assert.Equal(t, 1, calls)
}

func TestPaginateCE_CallbackErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	err := paginateCE(context.Background(), 5, "x", func(*string) (*string, error) { return nil, boom })
	assert.Equal(t, boom, err)
}

// endlessCeiling stops a regressed cap from spinning until the test timeout.
const endlessCeiling = 500

// endlessCE returns a non-empty NextPageToken for every coverage and
// utilization call.
type endlessCE struct {
	mockCostExplorerAPI
	covCalls                          atomic.Int32
	utilCalls                         atomic.Int32
	spCovCalls, costCalls, spRecCalls atomic.Int32
}

func (m *endlessCE) GetReservationCoverage(_ context.Context, _ *costexplorer.GetReservationCoverageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetReservationCoverageOutput, error) {
	n := m.covCalls.Add(1)
	if n > endlessCeiling {
		return nil, errors.New("mock: runaway pagination")
	}
	return &costexplorer.GetReservationCoverageOutput{NextPageToken: aws.String(fmt.Sprintf("t%d", n))}, nil
}

func (m *endlessCE) GetReservationUtilization(_ context.Context, _ *costexplorer.GetReservationUtilizationInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetReservationUtilizationOutput, error) {
	n := m.utilCalls.Add(1)
	if n > endlessCeiling {
		return nil, errors.New("mock: runaway pagination")
	}
	return &costexplorer.GetReservationUtilizationOutput{NextPageToken: aws.String(fmt.Sprintf("t%d", n))}, nil
}

func TestFetchCoveragePaged_CapsEndlessTokens(t *testing.T) {
	assert.Equal(t, 100, maxCoveragePages)
	mock := &endlessCE{}
	client := NewClientWithAPI(mock, "us-east-1")
	err := client.fetchCoveragePaged(context.Background(), &costexplorer.GetReservationCoverageInput{},
		func(string, string, PoolCoverage) { t.Fatal("partial results must not be recorded") }, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Equal(t, int32(maxCoveragePages), mock.covCalls.Load())
}

func TestGetRIUtilization_CapsEndlessTokens(t *testing.T) {
	assert.Equal(t, 100, maxUtilizationPages)
	mock := &endlessCE{}
	client := NewClientWithAPI(mock, "us-east-1")
	_, err := client.GetRIUtilization(context.Background(), 30, "us-east-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Equal(t, int32(maxUtilizationPages), mock.utilCalls.Load())
}

func TestGetDailyUsagePcts_CapsEndlessTokens(t *testing.T) {
	assert.Equal(t, 20, maxRecommendationPages)
	mock := &endlessCE{}
	client := NewClientWithAPI(mock, "us-east-1")
	_, err := client.GetDailyUsagePcts(context.Background(), "Amazon Elastic Compute Cloud - Compute", "m5.large", "us-east-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get daily coverage for")
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Equal(t, int32(maxRecommendationPages), mock.covCalls.Load())
}

func TestGetDailyUsagePcts_PreCanceledContext(t *testing.T) {
	mock := &endlessCE{}
	client := NewClientWithAPI(mock, "us-east-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetDailyUsagePcts(ctx, "Amazon Elastic Compute Cloud - Compute", "m5.large", "us-east-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, mock.covCalls.Load())
}

func (m *endlessCE) GetSavingsPlansCoverage(_ context.Context, _ *costexplorer.GetSavingsPlansCoverageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansCoverageOutput, error) {
	n := m.spCovCalls.Add(1)
	if n > endlessCeiling {
		return nil, errors.New("mock: runaway pagination")
	}
	return &costexplorer.GetSavingsPlansCoverageOutput{NextToken: aws.String(fmt.Sprintf("t%d", n))}, nil
}

func (m *endlessCE) GetCostAndUsage(_ context.Context, _ *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	n := m.costCalls.Add(1)
	if n > endlessCeiling {
		return nil, errors.New("mock: runaway pagination")
	}
	return &costexplorer.GetCostAndUsageOutput{NextPageToken: aws.String(fmt.Sprintf("t%d", n))}, nil
}

func (m *endlessCE) GetSavingsPlansPurchaseRecommendation(_ context.Context, _ *costexplorer.GetSavingsPlansPurchaseRecommendationInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error) {
	n := m.spRecCalls.Add(1)
	if n > endlessCeiling {
		return nil, errors.New("mock: runaway pagination")
	}
	return &costexplorer.GetSavingsPlansPurchaseRecommendationOutput{
		SavingsPlansPurchaseRecommendation: &types.SavingsPlansPurchaseRecommendation{},
		NextPageToken:                      aws.String(fmt.Sprintf("t%d", n)),
	}, nil
}

func TestGetSPCoveredSeries_CapsEndlessTokens(t *testing.T) {
	assert.Equal(t, 20, maxSPCoveragePages)
	mock := &endlessCE{}
	_, err := NewClientWithAPI(mock, "us-east-1").GetSPCoveredSeries(context.Background(), "us-east-1", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sp covered series: pagination cap reached after 20 pages")
	assert.Equal(t, int32(20), mock.spCovCalls.Load())
}

func TestGetOnDemandSeries_CapsEndlessTokens(t *testing.T) {
	assert.Equal(t, 20, maxOnDemandSeriesPages)
	mock := &endlessCE{}
	_, err := NewClientWithAPI(mock, "us-east-1").GetOnDemandSeries(context.Background(), "us-east-1", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GetOnDemandSeries: pagination cap reached after 20 pages")
	assert.Equal(t, int32(20), mock.costCalls.Load())
}

func TestGetSPCommitmentRate_CapsEndlessTokens(t *testing.T) {
	assert.Equal(t, 20, maxRecommendationPages)
	mock := &endlessCE{}
	_, err := NewClientWithAPI(mock, "us-east-1").GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeComputeSp))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sp commitment rate: pagination cap reached after 20 pages")
	assert.Equal(t, int32(20), mock.spRecCalls.Load())
}

func TestGetSPCommitmentRate_PreCanceledContext(t *testing.T) {
	mock := &endlessCE{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClientWithAPI(mock, "us-east-1").GetSPCommitmentRate(ctx, rateParams(types.SupportedSavingsPlansTypeComputeSp))
	assert.Equal(t, context.Canceled, err)
	assert.Zero(t, mock.spRecCalls.Load())
}

// scriptedCE serves SP recommendation pages from a script and fails
// GetCostAndUsage with costErr.
type scriptedCE struct {
	mockCostExplorerAPI
	spPages []*costexplorer.GetSavingsPlansPurchaseRecommendationOutput
	spErr   error
	costErr error
	spCalls int
}

func (m *scriptedCE) GetSavingsPlansPurchaseRecommendation(_ context.Context, _ *costexplorer.GetSavingsPlansPurchaseRecommendationInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error) {
	if m.spErr != nil {
		return nil, m.spErr
	}
	out := m.spPages[m.spCalls]
	m.spCalls++
	return out, nil
}

func (m *scriptedCE) GetCostAndUsage(_ context.Context, _ *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	return nil, m.costErr
}

func TestGetSPCommitmentRate_AbsentRecommendationReportsPageIndex(t *testing.T) {
	mock := &scriptedCE{spPages: []*costexplorer.GetSavingsPlansPurchaseRecommendationOutput{
		{SavingsPlansPurchaseRecommendation: &types.SavingsPlansPurchaseRecommendation{}, NextPageToken: aws.String("t1")},
		{},
	}}
	_, err := NewClientWithAPI(mock, "us-east-1").GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeComputeSp))
	require.ErrorIs(t, err, ErrNoSPCommitmentRate)
	assert.Contains(t, err.Error(), "page 1")
}

func TestGetSPCommitmentRate_FetchErrorKeepsPrefix(t *testing.T) {
	boom := errors.New("boom")
	mock := &scriptedCE{spErr: boom}
	_, err := NewClientWithAPI(mock, "us-east-1").GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeComputeSp))
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "sp commitment rate: ")
}

func TestGetOnDemandSeries_FetchErrorKeepsPrefix(t *testing.T) {
	boom := errors.New("boom")
	mock := &scriptedCE{costErr: boom}
	_, err := NewClientWithAPI(mock, "us-east-1").GetOnDemandSeries(context.Background(), "us-east-1", 7)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "GetOnDemandSeries: ")
}
