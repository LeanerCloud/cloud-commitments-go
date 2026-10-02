package recommendations

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

type incompleteCollectionAPI struct {
	mockCostExplorerAPI
	request  func(*costexplorer.GetReservationPurchaseRecommendationInput) (*costexplorer.GetReservationPurchaseRecommendationOutput, error)
	coverage func()
}

func (m *incompleteCollectionAPI) GetReservationPurchaseRecommendation(_ context.Context, in *costexplorer.GetReservationPurchaseRecommendationInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetReservationPurchaseRecommendationOutput, error) {
	return m.request(in)
}

func (m *incompleteCollectionAPI) GetReservationCoverage(_ context.Context, _ *costexplorer.GetReservationCoverageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetReservationCoverageOutput, error) {
	if m.coverage != nil {
		m.coverage()
	}
	return &costexplorer.GetReservationCoverageOutput{}, nil
}

func incompleteRIDetails(valid bool, bad int) []types.ReservationPurchaseRecommendation {
	var blocks []types.ReservationPurchaseRecommendation
	if valid {
		blocks = append(blocks, types.ReservationPurchaseRecommendation{RecommendationDetails: []types.ReservationPurchaseRecommendationDetail{{
			RecommendedNumberOfInstancesToPurchase: aws.String("2"), EstimatedMonthlyOnDemandCost: aws.String("30"),
			InstanceDetails: &types.InstanceDetails{RDSInstanceDetails: &types.RDSInstanceDetails{InstanceType: aws.String("db.t3.medium"), Region: aws.String("us-east-1")}},
		}}})
	}
	for range bad {
		blocks = append(blocks, types.ReservationPurchaseRecommendation{RecommendationDetails: []types.ReservationPurchaseRecommendationDetail{{RecommendedNumberOfInstancesToPurchase: aws.String("bad")}}})
	}
	return blocks
}

func TestCollectionCompleteness_Combos(t *testing.T) {
	apiFailure := errors.New("access denied")
	for _, tc := range []struct {
		name                               string
		valid                              bool
		bad, failed, rows, details, scopes int
		fatal                              bool
	}{
		{name: "valid", valid: true, rows: 6},
		{name: "empty"},
		{name: "mixed details", valid: true, bad: 2, rows: 6, details: 12},
		{name: "all invalid", bad: 2, details: 12},
		{name: "empty and API failure", failed: 1, scopes: 1},
		{name: "valid and API failure", valid: true, failed: 1, rows: 5, scopes: 1},
		{name: "invalid and API failure", bad: 2, failed: 5, details: 2, scopes: 5},
		{name: "total API failure", failed: 6, fatal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			api := &incompleteCollectionAPI{request: func(*costexplorer.GetReservationPurchaseRecommendationInput) (*costexplorer.GetReservationPurchaseRecommendationOutput, error) {
				calls++
				if calls <= tc.failed {
					return nil, apiFailure
				}
				return &costexplorer.GetReservationPurchaseRecommendationOutput{Recommendations: incompleteRIDetails(tc.valid, tc.bad)}, nil
			}}
			recs, err := NewClientWithAPI(api, "us-east-1").GetRecommendationsForService(context.Background(), common.ServiceRDS)
			require.Equal(t, 6, calls)
			require.Len(t, recs, tc.rows)
			var incomplete *IncompleteRecommendationsError
			if tc.fatal {
				require.ErrorIs(t, err, apiFailure)
				require.False(t, errors.As(err, &incomplete))
				return
			}
			if tc.details+tc.scopes == 0 {
				require.NoError(t, err)
				return
			}
			require.ErrorAs(t, err, &incomplete)
			require.Equal(t, tc.details, incomplete.FailedDetails)
			require.Equal(t, tc.scopes, incomplete.FailedScopes)
			require.Len(t, incomplete.Causes, tc.details+tc.scopes)
			for _, cause := range incomplete.Causes {
				require.Contains(t, cause.Error(), "service rds term ")
				require.Contains(t, cause.Error(), " payment ")
			}
			if tc.bad > 0 {
				block := 0
				if tc.valid {
					block = 1
				}
				require.Contains(t, err.Error(), fmt.Sprintf("block %d detail 0", block))
				require.Contains(t, err.Error(), fmt.Sprintf("block %d detail 0", block+1))
			}
			if tc.failed > 0 {
				require.ErrorIs(t, err, apiFailure)
			}
		})
	}
}

func TestCollectionCompleteness_NestedServices(t *testing.T) {
	detailFailure := errors.New("bad quantity")
	apiFailure := errors.New("access denied")
	partial := &IncompleteRecommendationsError{FailedDetails: 1, FailedScopes: 1, Causes: []error{detailFailure, apiFailure}}
	recs, err := mergeServiceResults(
		serviceResult{name: "RDS", recs: []common.Recommendation{{Count: 2}}, err: fmt.Errorf("wrapped: %w", partial)},
		serviceResult{name: "EC2", err: apiFailure},
	)
	outerRecs, outerErr := mergeServiceResults(serviceResult{name: "nested", recs: recs, err: err}, serviceResult{name: "empty"})
	var incomplete *IncompleteRecommendationsError
	require.ErrorAs(t, outerErr, &incomplete)
	require.Len(t, outerRecs, 1)
	require.Equal(t, 1, incomplete.FailedDetails)
	require.Equal(t, 2, incomplete.FailedScopes)
	require.Len(t, incomplete.Causes, 3)
	require.ErrorIs(t, outerErr, detailFailure)
	require.ErrorIs(t, outerErr, apiFailure)
	for _, cause := range incomplete.Causes {
		var nested *IncompleteRecommendationsError
		require.False(t, errors.As(cause, &nested))
	}
	_, emptyErr := mergeServiceResults(serviceResult{name: "invalid", err: partial})
	require.ErrorAs(t, emptyErr, &incomplete)
	require.Equal(t, 1, incomplete.FailedDetails)
}

func TestCollectionCompleteness_Cancellation(t *testing.T) {
	for _, terminal := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(terminal.Error(), func(t *testing.T) {
			calls := 0
			api := &incompleteCollectionAPI{request: func(*costexplorer.GetReservationPurchaseRecommendationInput) (*costexplorer.GetReservationPurchaseRecommendationOutput, error) {
				calls++
				return nil, fmt.Errorf("SDK: %w", terminal)
			}}
			recs, err := NewClientWithAPI(api, "us-east-1").GetRecommendationsForService(context.Background(), common.ServiceRDS)
			require.ErrorIs(t, err, terminal)
			require.Nil(t, recs)
			require.Equal(t, 1, calls)
			recs, err = mergeServiceResults(serviceResult{name: "valid", recs: []common.Recommendation{{Count: 1}}}, serviceResult{name: "canceled", err: terminal})
			require.ErrorIs(t, err, terminal)
			require.Nil(t, recs)
			var incomplete *IncompleteRecommendationsError
			require.False(t, errors.As(err, &incomplete))
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coverageCalls := 0
	api := &incompleteCollectionAPI{
		request: func(*costexplorer.GetReservationPurchaseRecommendationInput) (*costexplorer.GetReservationPurchaseRecommendationOutput, error) {
			return &costexplorer.GetReservationPurchaseRecommendationOutput{Recommendations: incompleteRIDetails(true, 1)}, nil
		},
		coverage: func() { coverageCalls++; cancel() },
	}
	recs, err := NewClientWithAPI(api, "us-east-1").GetRecommendationsForService(ctx, common.ServiceRDS)
	require.Positive(t, coverageCalls)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, recs)
}
