package ladder

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type spCommitmentRateSource interface {
	GetSPCommitmentRate(context.Context, recommendations.SPCommitmentRateParams) (float64, error)
}

// rateLayer is one layer whose commitment rate is fetched independently.
type rateLayer struct {
	layer  ladder.LayerType
	plan   types.SupportedSavingsPlansType
	region string
}

// GetCommitmentRates fetches one rate per layer; a layer without a rate is
// absent (hold) and never affects the others.
func (a *AWSLadder) GetCommitmentRates(ctx context.Context, scope ladder.Scope, term ladder.Term, payment ladder.PaymentOption) (map[ladder.LayerType]float64, error) {
	if err := a.validateScope(scope); err != nil {
		return nil, err
	}
	if err := term.Validate(); err != nil {
		return nil, fmt.Errorf("GetCommitmentRates: %w", err)
	}
	if err := payment.Validate(); err != nil {
		return nil, fmt.Errorf("GetCommitmentRates: %w", err)
	}
	if a.spRates == nil {
		return nil, errors.New("GetCommitmentRates: Savings Plan rate source not wired")
	}
	// Compute SP stays absent until the owner decides (#349). Its account-wide
	// blend over EC2+Lambda+Fargate biases sizing of a regional EC2-only gap.
	layers := []rateLayer{
		{layer: ladder.LayerEC2InstanceSP, plan: types.SupportedSavingsPlansTypeEc2InstanceSp, region: a.cfg.Region},
	}
	return fetchLayerRates(ctx, a.spRates, layers, recommendations.SPCommitmentRateParams{
		AccountID: a.cfg.AccountID,
		Term:      string(term), PaymentOption: string(payment),
		LookbackPeriod: fmt.Sprintf("%dd", a.cfg.lookbackDays()),
	})
}

// fetchLayerRates sets PlanType and Region from each layer on a copy of base.
func fetchLayerRates(ctx context.Context, src spCommitmentRateSource, layers []rateLayer, base recommendations.SPCommitmentRateParams) (map[ladder.LayerType]float64, error) {
	rates := make(map[ladder.LayerType]float64, len(layers))
	for _, l := range layers {
		p := base
		p.PlanType, p.Region = l.plan, l.region
		rate, err := src.GetSPCommitmentRate(ctx, p)
		if errors.Is(err, recommendations.ErrNoSPCommitmentRate) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GetCommitmentRates: layer %s: %w", l.layer, err)
		}
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
			return nil, fmt.Errorf("GetCommitmentRates: layer %s: unusable rate %v", l.layer, rate)
		}
		rates[l.layer] = rate
	}
	return rates, nil
}
