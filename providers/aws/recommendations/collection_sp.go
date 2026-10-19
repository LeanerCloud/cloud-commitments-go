package recommendations

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
)

func (c *Client) getSavingsPlansRecommendations(ctx context.Context, params *common.RecommendationParams) ([]common.Recommendation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	planTypes := planTypesForParams(params)
	if len(planTypes) == 0 {
		return []common.Recommendation{}, nil
	}
	payment, err := convertSavingsPlansPaymentOption(params.PaymentOption)
	if err != nil {
		return nil, fmt.Errorf("invalid payment option for Savings Plans recommendation: %w", err)
	}
	term, err := convertSavingsPlansTermInYears(params.Term)
	if err != nil {
		return nil, fmt.Errorf("invalid term for Savings Plans recommendation: %w", err)
	}
	lookback, err := convertSavingsPlansLookbackPeriod(params.LookbackPeriod)
	if err != nil {
		return nil, fmt.Errorf("invalid lookback period for Savings Plans recommendation: %w", err)
	}
	scope, err := convertSavingsPlansAccountScope(params.SPAccountScope)
	if err != nil {
		return nil, err
	}
	results := make([]serviceResult, 0, len(planTypes))
	for _, planType := range planTypes {
		input := &costexplorer.GetSavingsPlansPurchaseRecommendationInput{
			SavingsPlansType: planType, PaymentOption: payment, TermInYears: term,
			LookbackPeriodInDays: lookback, AccountScope: scope,
		}
		recs, fetchErr := c.fetchSPAllPages(ctx, input, params, planType)
		if canceled := collectionCancellation(fetchErr); canceled != nil {
			return nil, canceled
		}
		results = append(results, serviceResult{
			name: fmt.Sprintf("%s term %s payment %s plan %s", params.Service, params.Term, params.PaymentOption, planType),
			recs: recs, err: fetchErr,
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return mergeServiceResults(results...)
}

// convertSavingsPlansAccountScope maps RecommendationParams.SPAccountScope to
// the Cost Explorer scope. Empty is linked, the long-standing default; an
// unknown value is an error rather than a silent fallback.
func convertSavingsPlansAccountScope(s string) (types.AccountScope, error) {
	return convertAccountScope(s, "Savings Plans")
}

// convertAccountScope is shared by the Savings Plans and RI recommendation
// paths so both accept the same values.
func convertAccountScope(s, kind string) (types.AccountScope, error) {
	switch strings.ToLower(s) {
	case "", "linked":
		return types.AccountScopeLinked, nil
	case "payer":
		return types.AccountScopePayer, nil
	}
	return "", fmt.Errorf("invalid %s account scope %q: want linked or payer", kind, s)
}

func (c *Client) fetchSPAllPages(
	ctx context.Context,
	input *costexplorer.GetSavingsPlansPurchaseRecommendationInput,
	params *common.RecommendationParams,
	planType types.SupportedSavingsPlansType,
) ([]common.Recommendation, error) {
	var pages []serviceResult
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scope := serviceResult{name: fmt.Sprintf("%s term %s payment %s plan %s page %d",
			params.Service, params.Term, params.PaymentOption, planType, page)}
		if page >= maxRecommendationPages {
			scope.err = fmt.Errorf("pagination cap reached after %d pages for SP %s (issue #692)", maxRecommendationPages, planType)
			pages = append(pages, scope)
			break
		}
		result, err := c.fetchSPPageWithRetry(ctx, input)
		if canceled := collectionCancellation(err); canceled != nil {
			return nil, canceled
		}
		scope.err = err
		if err != nil {
			pages = append(pages, scope)
			break
		}
		if result == nil {
			pages = append(pages, scope)
			break
		}
		if result.SavingsPlansPurchaseRecommendation != nil {
			scope.recs, scope.err = c.parseSavingsPlansRecommendations(result.SavingsPlansPurchaseRecommendation, params, planType, page)
		}
		pages = append(pages, scope)
		input.NextPageToken = result.NextPageToken
		if aws.ToString(input.NextPageToken) == "" {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return mergeServiceResults(pages...)
}

func (c *Client) fetchSPPageWithRetry(
	ctx context.Context,
	input *costexplorer.GetSavingsPlansPurchaseRecommendationInput,
) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error) {
	rateLimiter := c.rateLimiter.newOperation()
	var result *costexplorer.GetSavingsPlansPurchaseRecommendationOutput
	var err error

	for {
		if waitErr := rateLimiter.Wait(ctx); waitErr != nil {
			return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
		}

		if acqErr := concurrency.Acquire(ctx); acqErr != nil {
			return nil, fmt.Errorf("concurrency acquire failed: %w", acqErr)
		}
		result, err = c.costExplorerClient.GetSavingsPlansPurchaseRecommendation(ctx, input)
		concurrency.Release(ctx)
		if !rateLimiter.ShouldRetry(err) {
			break
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}

	return result, nil
}
