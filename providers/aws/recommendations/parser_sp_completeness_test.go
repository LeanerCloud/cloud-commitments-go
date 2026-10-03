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

func TestSPCompletenessSDKPageCap(t *testing.T) {
	t.Parallel()
	for _, lastToken := range []string{"", "next"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("token=%s/empty=%t", lastToken, empty), func(t *testing.T) {
				t.Parallel()
				calls := 0
				fixture := &spSDKFixture{respond: func(spSDKRequest) (map[string]any, error) {
					calls++
					token := "next"
					if calls == maxRecommendationPages {
						token = lastToken
					}
					if empty {
						return spSDKPage(token), nil
					}
					return spSDKPage(token, spSDKDetail(fmt.Sprint(calls))), nil
				}}
				recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), spSDKParams(common.ServiceSavingsPlansCompute))
				if lastToken == "" {
					require.NoError(t, err)
				} else {
					assertSPIncomplete(t, err, 0, 1)
					require.Contains(t, err.Error(), "pagination cap reached")
				}
				require.Equal(t, maxRecommendationPages, calls)
				if empty {
					require.Empty(t, recs)
				} else {
					require.Len(t, recs, maxRecommendationPages)
				}
			})
		}
	}
}

func TestSPCompletenessSDKRouting(t *testing.T) {
	t.Parallel()
	for service, plan := range map[common.ServiceType]string{
		common.ServiceSavingsPlansCompute: "COMPUTE_SP", common.ServiceSavingsPlansEC2Instance: "EC2_INSTANCE_SP",
		common.ServiceSavingsPlansSageMaker: "SAGEMAKER_SP", common.ServiceSavingsPlansDatabase: "DATABASE_SP",
	} {
		t.Run(string(service), func(t *testing.T) {
			t.Parallel()
			fixture := &spSDKFixture{respond: func(spSDKRequest) (map[string]any, error) { return spSDKPage(""), nil }}
			params := spSDKParams(service)
			params.IncludeSPTypes = []string{"unmatched"}
			params.ExcludeSPTypes = []string{"compute", "ec2instance", "sagemaker", "database"}
			recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), params)
			require.NoError(t, err)
			require.Empty(t, recs)
			require.Len(t, fixture.requests, 1)
			require.Equal(t, plan, fixture.requests[0].SavingsPlansType)
		})
	}
	for _, include := range [][]string{nil, {"compute", "database"}, {"unmatched"}} {
		t.Run(fmt.Sprint(include), func(t *testing.T) {
			t.Parallel()
			fixture := &spSDKFixture{respond: func(spSDKRequest) (map[string]any, error) { return spSDKPage(""), nil }}
			params := spSDKParams(common.ServiceSavingsPlansAll)
			params.IncludeSPTypes, params.ExcludeSPTypes = include, []string{"database"}
			recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), params)
			require.NoError(t, err)
			require.Empty(t, recs)
			got := make([]string, 0, len(fixture.requests))
			for _, request := range fixture.requests {
				got = append(got, request.SavingsPlansType)
			}
			want := []string{"COMPUTE_SP", "EC2_INSTANCE_SP", "SAGEMAKER_SP"}
			if len(include) == 2 {
				want = want[:1]
			}
			if len(include) == 1 {
				want = want[:0]
			}
			require.Equal(t, want, got)
		})
	}
}

func TestSPCompletenessSDKCancellation(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"before", "between pages", "between types", "final response", "SDK canceled", "SDK deadline"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			fixture := &spSDKFixture{respond: func(spSDKRequest) (map[string]any, error) {
				if stage == "SDK canceled" {
					return nil, fmt.Errorf("fixture: %w", context.Canceled)
				}
				if stage == "SDK deadline" {
					return nil, fmt.Errorf("fixture: %w", context.DeadlineExceeded)
				}
				cancel()
				token := ""
				if stage == "between pages" {
					token = "next"
				}
				return spSDKPage(token, spSDKDetail("discarded")), nil
			}}
			if stage == "before" {
				cancel()
			}
			if stage == "SDK deadline" {
				want = context.DeadlineExceeded
			}
			params := spSDKParams(common.ServiceSavingsPlansCompute)
			if stage == "between types" {
				params.Service = common.ServiceSavingsPlansAll
			}
			recs, err := spSDKClient(fixture).GetRecommendations(ctx, params)
			require.ErrorIs(t, err, want)
			require.Nil(t, recs)
			var partial *IncompleteRecommendationsError
			require.False(t, errors.As(err, &partial))
			calls := 1
			if stage == "before" {
				calls = 0
			}
			require.Len(t, fixture.requests, calls)
		})
	}
}

type spIncompleteAPI struct {
	mockCostExplorerForSP
	respond func(*costexplorer.GetSavingsPlansPurchaseRecommendationInput) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error)
}

func (m *spIncompleteAPI) GetSavingsPlansPurchaseRecommendation(_ context.Context, in *costexplorer.GetSavingsPlansPurchaseRecommendationInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error) {
	return m.respond(in)
}

func TestSPCompletenessRetainsSentinelAndDetails(t *testing.T) {
	t.Parallel()
	failure := errors.New("page failed")
	calls := 0
	api := &spIncompleteAPI{respond: func(in *costexplorer.GetSavingsPlansPurchaseRecommendationInput) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error) {
		calls++
		if in.NextPageToken != nil {
			return nil, failure
		}
		return &costexplorer.GetSavingsPlansPurchaseRecommendationOutput{NextPageToken: aws.String("next"),
			SavingsPlansPurchaseRecommendation: &types.SavingsPlansPurchaseRecommendation{SavingsPlansPurchaseRecommendationDetails: []types.SavingsPlansPurchaseRecommendationDetail{
				{HourlyCommitmentToPurchase: aws.String("bad"), UpfrontCost: aws.String("also bad")},
			}}}, nil
	}}
	recs, err := NewClientWithAPI(api, "us-east-1").GetRecommendations(context.Background(), spSDKParams(common.ServiceSavingsPlansCompute))
	assertSPIncomplete(t, err, 1, 1)
	require.ErrorIs(t, err, failure)
	require.Empty(t, recs)
	require.Equal(t, 2, calls)
}
