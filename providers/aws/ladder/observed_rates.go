package ladder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type observedRateSource interface {
	GetSPUtilizationForAccount(ctx context.Context, planType types.SupportedSavingsPlansType, accountID, region string, lookbackDays int) (recommendations.SPUtilizationSummary, error)
	GetRIFeeUtilization(ctx context.Context, accountID, region string, lookbackDays int) ([]recommendations.RIFeeUtilization, error)
}

// GetObservedCommitmentRates measures commitment dollars per covered on-demand
// dollar on this account's existing commitments over the lookback window. A
// layer with no usable observation is absent; read failures are errors.
func (a *AWSLadder) GetObservedCommitmentRates(ctx context.Context, scope ladder.Scope) (map[ladder.LayerType]float64, error) {
	if err := a.validateScope(scope); err != nil {
		return nil, err
	}
	if a.observed == nil {
		return nil, errors.New("GetObservedCommitmentRates: observed rate source not wired")
	}
	rates := map[ladder.LayerType]float64{}
	for _, sp := range []struct {
		layer  ladder.LayerType
		plan   types.SupportedSavingsPlansType
		region string
	}{
		{ladder.LayerEC2InstanceSP, types.SupportedSavingsPlansTypeEc2InstanceSp, a.cfg.Region},
		{ladder.LayerComputeSP, types.SupportedSavingsPlansTypeComputeSp, ""},
	} {
		summary, err := a.observed.GetSPUtilizationForAccount(ctx, sp.plan, a.cfg.AccountID, sp.region, a.cfg.lookbackDays())
		if err != nil {
			return nil, fmt.Errorf("GetObservedCommitmentRates: %s: %w", sp.layer, err)
		}
		k, err := summary.CommitmentToOnDemandRatio()
		switch {
		case errors.Is(err, recommendations.ErrNoSPUtilization):
		case err != nil:
			log.Printf("WARNING: AWSLadder GetObservedCommitmentRates: dropping %s: %v", sp.layer, err)
		default:
			rates[sp.layer] = k
		}
	}
	riRate, ok, err := a.observedConvertibleRIRate(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		rates[ladder.LayerConvertibleRI] = riRate
	}
	return rates, nil
}

// observedConvertibleRIRate is sum(fee * actual/purchased) / sum(on-demand cost
// of used hours) over the convertible RIs: the used-hours share of the fee, so
// unused hours do not push the rate above 1.
func (a *AWSLadder) observedConvertibleRIRate(ctx context.Context) (rate float64, ok bool, err error) {
	ris, err := a.ris.ListConvertibleReservedInstances(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("GetObservedCommitmentRates: list convertible RIs: %w", err)
	}
	if len(ris) == 0 {
		return 0, false, nil // skip the billed CE call: nothing to join
	}
	fees, err := a.observed.GetRIFeeUtilization(ctx, a.cfg.AccountID, a.cfg.Region, a.cfg.lookbackDays())
	if err != nil {
		return 0, false, fmt.Errorf("GetObservedCommitmentRates: %s: %w", ladder.LayerConvertibleRI, err)
	}
	usedFee, onDemand, ok := sumUsedRIFees(fees, convertibleRIIDs(ris))
	if !ok || onDemand == 0 {
		return 0, false, nil
	}
	r := usedFee / onDemand
	if math.IsNaN(r) || r <= 0 || r > 1 {
		log.Printf("WARNING: AWSLadder GetObservedCommitmentRates: dropping %s: ratio %g outside (0, 1]", ladder.LayerConvertibleRI, r)
		return 0, false, nil
	}
	return r, true, nil
}

// sumUsedRIFees totals the used-hours fee and on-demand cost over the RIs in
// ids. ok is false when a convertible RI has hours or cost without purchased
// hours, or more actual than purchased hours, so the layer degrades to the
// marginal rate instead of failing the run.
func sumUsedRIFees(fees []recommendations.RIFeeUtilization, ids map[string]struct{}) (usedFee, onDemand float64, ok bool) {
	for _, f := range fees {
		if _, in := ids[f.ReservedInstanceID]; !in {
			continue
		}
		// Actual hours with zero purchased hours fall under actual > purchased.
		if f.PurchasedHours == 0 && (f.TotalAmortizedFee > 0 || f.OnDemandCostOfRIHoursUsed > 0) ||
			f.TotalActualHours > f.PurchasedHours {
			log.Printf("WARNING: AWSLadder GetObservedCommitmentRates: dropping %s: RI %s has malformed hours (purchased=%g actual=%g)",
				ladder.LayerConvertibleRI, f.ReservedInstanceID, f.PurchasedHours, f.TotalActualHours)
			return 0, 0, false
		}
		if f.PurchasedHours == 0 {
			continue
		}
		usedFee += f.TotalAmortizedFee * f.TotalActualHours / f.PurchasedHours
		onDemand += f.OnDemandCostOfRIHoursUsed
	}
	return usedFee, onDemand, true
}
