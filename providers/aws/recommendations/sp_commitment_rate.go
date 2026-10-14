package recommendations

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// ErrNoSPCommitmentRate means Cost Explorer produced no usable Savings Plan
// recommendation for the requested scope (no matching detail, a field the
// formula needs is absent, or no covered on-demand spend). Callers must treat
// the layer as having no rate (explainable Hold); they must never substitute a
// 1:1 rate.
var ErrNoSPCommitmentRate = errors.New("sp commitment rate: no usable recommendation detail")

// SPCommitmentRateParams selects the recommendation details a rate is derived
// from. All fields are required.
type SPCommitmentRateParams struct {
	// PlanType is COMPUTE_SP or EC2_INSTANCE_SP.
	PlanType types.SupportedSavingsPlansType
	// AccountID restricts details to this (linked) account.
	AccountID string
	// Region restricts details to this region. CE reports it on
	// SavingsPlansDetails only for EC2 Instance SP; Compute SP details carry no
	// region, so for COMPUTE_SP the result is the account-wide blend across
	// EC2, Lambda and Fargate.
	Region string
	// Term is 1yr or 3yr; PaymentOption and LookbackPeriod use the same
	// vocabulary as common.RecommendationParams.
	Term           string
	PaymentOption  string
	LookbackPeriod string
}

// GetSPCommitmentRate returns r: commitment dollars per on-demand dollar for
// the USED part of a new Savings Plan, derived from CE purchase
// recommendations.
//
// EstimatedSavingsPercentage is measured against ALL applicable on-demand
// spend, not just the covered part, so it cannot be used. Per detail:
//
//	covered_od = EstimatedSPCost + EstimatedSavingsAmount
//	r          = (EstimatedAverageUtilization/100) * EstimatedSPCost / covered_od
//
// summed over the matching details (ratio of sums; every detail shares the
// lookback so the hours cancel). The raw detail is parsed strictly: an absent
// field is ErrNoSPCommitmentRate (never degraded to 0, which would give
// r == utilization ~ 1:1), an unparseable or non-finite one is an error, and
// a non-positive r (zero utilization) is an error; r <= 1 holds by construction
// (non-negative savings, utilization <= 100).
//
// MANUAL VERIFICATION REQUIRED before relying on it with real money: the
// semantics of the CE cost fields are verified only against the SDK docs and
// a constructed CE-shaped fixture, not a live response.
func (c *Client) GetSPCommitmentRate(ctx context.Context, p SPCommitmentRateParams) (float64, error) {
	input, err := spRateInput(p)
	if err != nil {
		return 0, err
	}
	spCost, covered, matched, err := c.sumSPRateDetails(ctx, input, &p)
	if err != nil {
		return 0, err
	}
	if matched == 0 || covered <= 0 {
		return 0, fmt.Errorf("%w: plan=%s region=%q term=%s payment=%s (%d matching details)",
			ErrNoSPCommitmentRate, p.PlanType, p.Region, p.Term, p.PaymentOption, matched)
	}
	r := spCost / covered
	if math.IsNaN(r) || r <= 0 {
		return 0, fmt.Errorf("sp commitment rate: r=%g is not positive (used spend %g, covered on-demand %g)", r, spCost, covered)
	}
	return r, nil
}

// sumSPRateDetails pages through the recommendation and sums the used SP spend
// and covered on-demand spend of every matching detail.
func (c *Client) sumSPRateDetails(ctx context.Context, input *costexplorer.GetSavingsPlansPurchaseRecommendationInput, p *SPCommitmentRateParams) (spCost, covered float64, matched int, err error) {
	for page := 0; ; page++ {
		if page >= maxRecommendationPages {
			return 0, 0, 0, fmt.Errorf("sp commitment rate: pagination cap reached after %d pages", maxRecommendationPages)
		}
		out, fetchErr := c.fetchSPPageWithRetry(ctx, input)
		if fetchErr != nil {
			return 0, 0, 0, fmt.Errorf("sp commitment rate: %w", fetchErr)
		}
		if out == nil || out.SavingsPlansPurchaseRecommendation == nil {
			return 0, 0, 0, fmt.Errorf("%w: recommendation absent on page %d", ErrNoSPCommitmentRate, page)
		}
		rec := out.SavingsPlansPurchaseRecommendation
		for i := range rec.SavingsPlansPurchaseRecommendationDetails {
			d := &rec.SavingsPlansPurchaseRecommendationDetails[i]
			if !spRateDetailMatches(d, p) {
				continue
			}
			used, cov, err := spRateTerms(d)
			if err != nil {
				return 0, 0, 0, err
			}
			spCost += used
			covered += cov
			matched++
		}
		input.NextPageToken = out.NextPageToken
		if aws.ToString(input.NextPageToken) == "" {
			break
		}
	}
	return spCost, covered, matched, nil
}

func spRateInput(p SPCommitmentRateParams) (*costexplorer.GetSavingsPlansPurchaseRecommendationInput, error) {
	if p.PlanType != types.SupportedSavingsPlansTypeComputeSp && p.PlanType != types.SupportedSavingsPlansTypeEc2InstanceSp {
		return nil, fmt.Errorf("sp commitment rate: unsupported plan type %q", p.PlanType)
	}
	if p.AccountID == "" || p.Region == "" {
		return nil, errors.New("sp commitment rate: AccountID and Region are required")
	}
	payment, err := convertSavingsPlansPaymentOption(p.PaymentOption)
	if err != nil {
		return nil, fmt.Errorf("sp commitment rate: payment option: %w", err)
	}
	term, err := convertSavingsPlansTermInYears(p.Term)
	if err != nil {
		return nil, fmt.Errorf("sp commitment rate: term: %w", err)
	}
	lookback, err := convertSavingsPlansLookbackPeriod(p.LookbackPeriod)
	if err != nil {
		return nil, fmt.Errorf("sp commitment rate: lookback: %w", err)
	}
	return &costexplorer.GetSavingsPlansPurchaseRecommendationInput{
		SavingsPlansType: p.PlanType, PaymentOption: payment, TermInYears: term,
		LookbackPeriodInDays: lookback, AccountScope: types.AccountScopeLinked,
	}, nil
}

// spRateDetailMatches filters details to the requested account and, for EC2
// Instance SP, region.
func spRateDetailMatches(d *types.SavingsPlansPurchaseRecommendationDetail, p *SPCommitmentRateParams) bool {
	if aws.ToString(d.AccountId) != p.AccountID {
		return false
	}
	if p.PlanType != types.SupportedSavingsPlansTypeEc2InstanceSp {
		return true
	}
	return d.SavingsPlansDetails != nil &&
		normalizeRegionName(aws.ToString(d.SavingsPlansDetails.Region)) == p.Region
}

// spRateTerms returns the used SP spend and the covered on-demand spend of one
// detail, parsed strictly.
func spRateTerms(d *types.SavingsPlansPurchaseRecommendationDetail) (usedSPCost, coveredOD float64, err error) {
	spCost, err := spRateField("EstimatedSPCost", d.EstimatedSPCost)
	if err != nil {
		return 0, 0, err
	}
	savings, err := spRateField("EstimatedSavingsAmount", d.EstimatedSavingsAmount)
	if err != nil {
		return 0, 0, err
	}
	utilPct, err := spRateField("EstimatedAverageUtilization", d.EstimatedAverageUtilization)
	if err != nil {
		return 0, 0, err
	}
	if utilPct > 100 {
		return 0, 0, fmt.Errorf("sp commitment rate: EstimatedAverageUtilization %g exceeds 100", utilPct)
	}
	return utilPct / 100 * spCost, spCost + savings, nil
}

// spRateField parses a required non-negative finite CE string field. Absent is
// ErrNoSPCommitmentRate; present-but-invalid is a plain error.
func spRateField(name string, v *string) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("%w: %s absent", ErrNoSPCommitmentRate, name)
	}
	f, err := strconv.ParseFloat(*v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("sp commitment rate: %s %q is not a non-negative finite number", name, *v)
	}
	return f, nil
}
