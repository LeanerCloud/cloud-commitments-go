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

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// ErrNoRICommitmentRate means Cost Explorer produced no usable Reserved
// Instance recommendation for the requested scope (no matching detail, a field
// the formula needs is absent, or no covered on-demand spend). Callers must
// treat the layer as having no rate (explainable Hold); they must never
// substitute a 1:1 rate.
var ErrNoRICommitmentRate = errors.New("ri commitment rate: no usable recommendation detail")

// RICommitmentRateParams selects the recommendation details a rate is derived
// from. All fields are required.
type RICommitmentRateParams struct {
	// AccountID restricts details to this (linked) account.
	AccountID string
	// Region restricts details to this region. CE reports it only on
	// InstanceDetails.EC2InstanceDetails, possibly as a display name.
	Region string
	// Term is 1yr or 3yr; PaymentOption and LookbackPeriod use the same
	// vocabulary as common.RecommendationParams.
	Term           string
	PaymentOption  string
	LookbackPeriod string
}

// GetRICommitmentRate returns r: commitment dollars per on-demand dollar for
// the USED part of a new Convertible EC2 Reserved Instance, derived from CE
// purchase recommendations. It costs one billed CE request per result page.
//
// The CE cost fields are monthly USD for the whole recommended quantity and
// UpfrontCost is the one-time upfront for the term, so per detail:
//
//	eff        = RecurringStandardMonthlyCost + UpfrontCost/(12*termYears)
//	covered_od = eff + EstimatedMonthlySavingsAmount
//	r          = (AverageUtilization/100) * eff / covered_od
//
// summed over the matching details (ratio of sums, so a one-instance detail
// does not outweigh a hundred-instance one). EstimatedMonthlySavingsPercentage
// and EstimatedMonthlyOnDemandCost are not used: the first is measured against
// all on-demand spend and the meaning of the second is not documented.
// Quantities are CE's reported ones, not the rounded-down counts of the
// parser; the rate does not depend on quantity, so rounding only reweights
// details negligibly.
//
// Absent fields are ErrNoRICommitmentRate, never degraded to 0 (which would
// push r towards the utilization), with one payment-specific exception:
// UpfrontCost may be absent under no-upfront and the recurring charge under
// all-upfront. Unparseable, non-finite or negative values are plain errors,
// as is a non-positive r. r <= 1 holds by construction. Underestimating r only
// buys less, so every ambiguity resolves towards the lower r.
//
// MANUAL VERIFICATION REQUIRED before relying on it with real money: the
// semantics of the CE cost fields are verified only against the SDK docs and
// a constructed CE-shaped fixture, not a live response.
func (c *Client) GetRICommitmentRate(ctx context.Context, p RICommitmentRateParams) (float64, error) {
	input, months, err := riRateInput(p)
	if err != nil {
		return 0, err
	}
	sums, err := c.sumRIRateDetails(ctx, input, &p, months)
	if err != nil {
		return 0, err
	}
	if sums.matched == 0 || sums.covered <= 0 {
		return 0, fmt.Errorf("%w: region=%q term=%s payment=%s (%d matching details)",
			ErrNoRICommitmentRate, p.Region, p.Term, p.PaymentOption, sums.matched)
	}
	r := sums.used / sums.covered
	if math.IsNaN(r) || r <= 0 {
		return 0, fmt.Errorf("ri commitment rate: r=%g is not positive (used cost %g, covered on-demand %g)", r, sums.used, sums.covered)
	}
	return r, nil
}

// riRateSums accumulates the used effective RI cost and the covered on-demand
// cost over matching details.
type riRateSums struct {
	used, covered float64
	matched       int
}

func riRateInput(p RICommitmentRateParams) (*costexplorer.GetReservationPurchaseRecommendationInput, float64, error) {
	if p.AccountID == "" || p.Region == "" {
		return nil, 0, errors.New("ri commitment rate: AccountID and Region are required")
	}
	payment, err := convertPaymentOptionE(p.PaymentOption)
	if err != nil {
		return nil, 0, fmt.Errorf("ri commitment rate: payment option: %w", err)
	}
	term, months, err := riRequestTerm(p.Term)
	if err != nil {
		return nil, 0, fmt.Errorf("ri commitment rate: term: %w", err)
	}
	lookback, err := convertLookbackPeriodE(p.LookbackPeriod)
	if err != nil {
		return nil, 0, fmt.Errorf("ri commitment rate: lookback: %w", err)
	}
	return &costexplorer.GetReservationPurchaseRecommendationInput{
		Service:              aws.String(getServiceStringForCostExplorer(common.ServiceEC2)),
		PaymentOption:        payment,
		TermInYears:          term,
		LookbackPeriodInDays: lookback,
		AccountScope:         types.AccountScopeLinked,
		ServiceSpecification: &types.ServiceSpecification{
			EC2Specification: &types.EC2Specification{OfferingClass: ec2RecommendationOfferingClass},
		},
	}, months, nil
}

// riRequestTerm maps a term to the CE request value and the months the upfront
// is amortized over from one switch, so the two cannot diverge.
func riRequestTerm(term string) (types.TermInYears, float64, error) {
	switch common.Term(term) {
	case common.Term1Year, "1":
		return types.TermInYearsOneYear, 12, nil
	case common.Term3Year, "3":
		return types.TermInYearsThreeYears, 36, nil
	}
	return "", 0, fmt.Errorf("unsupported term %q: must be one of 1yr, 1, 3yr, 3", term)
}

// sumRIRateDetails pages through the recommendation and sums the used and
// covered cost of every matching detail.
func (c *Client) sumRIRateDetails(ctx context.Context, input *costexplorer.GetReservationPurchaseRecommendationInput, p *RICommitmentRateParams, months float64) (riRateSums, error) {
	var sums riRateSums
	err := paginateCE(ctx, maxRecommendationPages, "ri commitment rate", func(token *string) (*string, error) {
		input.NextPageToken = token
		out, fetchErr := c.fetchRIPageWithRetry(ctx, input)
		if fetchErr != nil {
			return nil, fmt.Errorf("ri commitment rate: %w", fetchErr)
		}
		if out == nil {
			return nil, fmt.Errorf("%w: empty response", ErrNoRICommitmentRate)
		}
		if addErr := addRIRatePage(out, p, input.PaymentOption, months, &sums); addErr != nil {
			return nil, addErr
		}
		return out.NextPageToken, nil
	})
	if err != nil {
		return riRateSums{}, err
	}
	return sums, nil
}

// addRIRatePage adds the matching details of one page to sums.
func addRIRatePage(out *costexplorer.GetReservationPurchaseRecommendationOutput, p *RICommitmentRateParams, payment types.PaymentOption, months float64, sums *riRateSums) error {
	for i := range out.Recommendations {
		details := out.Recommendations[i].RecommendationDetails
		for j := range details {
			d := &details[j]
			if !riRateDetailMatches(d, p) {
				continue
			}
			used, covered, err := riRateTerms(d, payment, months)
			if err != nil {
				return err
			}
			sums.used += used
			sums.covered += covered
			sums.matched++
		}
	}
	return nil
}

// riRateDetailMatches filters details to the requested account and region. A
// detail with no EC2 details or no region cannot be attributed to the region
// and is excluded.
func riRateDetailMatches(d *types.ReservationPurchaseRecommendationDetail, p *RICommitmentRateParams) bool {
	if aws.ToString(d.AccountId) != p.AccountID {
		return false
	}
	if d.InstanceDetails == nil || d.InstanceDetails.EC2InstanceDetails == nil {
		return false
	}
	return normalizeRegionName(aws.ToString(d.InstanceDetails.EC2InstanceDetails.Region)) == p.Region
}

// riRateTerms returns the used effective cost and the covered on-demand cost
// of one detail, parsed strictly.
func riRateTerms(d *types.ReservationPurchaseRecommendationDetail, payment types.PaymentOption, months float64) (used, covered float64, err error) {
	savings, err := riRateField("EstimatedMonthlySavingsAmount", d.EstimatedMonthlySavingsAmount, false)
	if err != nil {
		return 0, 0, err
	}
	utilPct, err := riRateField("AverageUtilization", d.AverageUtilization, false)
	if err != nil {
		return 0, 0, err
	}
	if utilPct > 100 {
		return 0, 0, fmt.Errorf("ri commitment rate: AverageUtilization %g exceeds 100", utilPct)
	}
	recurring, err := riRateField("RecurringStandardMonthlyCost", d.RecurringStandardMonthlyCost, payment == types.PaymentOptionAllUpfront)
	if err != nil {
		return 0, 0, err
	}
	upfront, err := riRateField("UpfrontCost", d.UpfrontCost, payment == types.PaymentOptionNoUpfront)
	if err != nil {
		return 0, 0, err
	}
	eff := recurring + upfront/months
	return utilPct / 100 * eff, eff + savings, nil
}

// riRateField parses a non-negative finite CE string field like spRateField.
// Absent is ErrNoRICommitmentRate unless the payment option makes it
// contractually zero; present-but-invalid is a plain error.
func riRateField(name string, v *string, zeroIfAbsent bool) (float64, error) {
	if v == nil {
		if zeroIfAbsent {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: %s absent", ErrNoRICommitmentRate, name)
	}
	f, err := strconv.ParseFloat(*v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("ri commitment rate: %s %q is not a non-negative finite number", name, *v)
	}
	return f, nil
}
