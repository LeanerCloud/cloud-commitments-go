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

type riCommitmentRateSource interface {
	GetRICommitmentRate(context.Context, recommendations.RICommitmentRateParams) (float64, error)
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
	if err := a.requireRateSources(); err != nil {
		return nil, err
	}
	rates, err := a.sizingSPRates(ctx, term, payment)
	if err != nil {
		return nil, err
	}
	rate, ok, err := a.convertibleRIRate(ctx, term, payment)
	if err != nil {
		return nil, err
	}
	if ok {
		rates[ladder.LayerConvertibleRI] = rate
	}
	return rates, nil
}

func (a *AWSLadder) requireRateSources() error {
	if a.spRates == nil {
		return errors.New("GetCommitmentRates: Savings Plan rate source not wired")
	}
	if a.riRates == nil {
		return errors.New("GetCommitmentRates: Reserved Instance rate source not wired")
	}
	return nil
}

func (a *AWSLadder) sizingSPRates(ctx context.Context, term ladder.Term, payment ladder.PaymentOption) (map[ladder.LayerType]float64, error) {
	// The Compute SP rate is Cost Explorer's account-wide blend over EC2, Lambda
	// and Fargate in all regions, so for a regional EC2-only gap it can over-buy
	// by r_blend/r_EC2. Accepted by the owner (#349, option A). Here only
	// MaxHourlyCommitPerRun bounds it, and only when set; the platform plans a
	// required per-sweep cap (cloud-commitments-platform#70). Region is required
	// by the source, not a filter.
	layers := []rateLayer{
		{layer: ladder.LayerEC2InstanceSP, plan: types.SupportedSavingsPlansTypeEc2InstanceSp, region: a.cfg.Region},
		{layer: ladder.LayerComputeSP, plan: types.SupportedSavingsPlansTypeComputeSp, region: a.cfg.Region},
	}
	return fetchLayerRates(ctx, a.spRates, layers, recommendations.SPCommitmentRateParams{
		AccountID: a.cfg.AccountID,
		Term:      string(term), PaymentOption: string(payment),
		LookbackPeriod: fmt.Sprintf("%dd", a.cfg.lookbackDays()),
	})
}

// convertibleRIRate fetches the region-filtered, Convertible-only new-purchase
// RI rate; ok is false when Cost Explorer has no usable recommendation. It
// costs one billed Cost Explorer request per result page.
func (a *AWSLadder) convertibleRIRate(ctx context.Context, term ladder.Term, payment ladder.PaymentOption) (rate float64, ok bool, err error) {
	rate, err = a.riRates.GetRICommitmentRate(ctx, recommendations.RICommitmentRateParams{
		AccountID: a.cfg.AccountID, Region: a.cfg.Region,
		Term: string(term), PaymentOption: string(payment),
		LookbackPeriod: fmt.Sprintf("%dd", a.cfg.lookbackDays()),
	})
	if errors.Is(err, recommendations.ErrNoRICommitmentRate) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("GetCommitmentRates: layer %s: %w", ladder.LayerConvertibleRI, err)
	}
	if err := checkUsableRate(ladder.LayerConvertibleRI, rate); err != nil {
		return 0, false, err
	}
	return rate, true, nil
}

func checkUsableRate(layer ladder.LayerType, rate float64) error {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return fmt.Errorf("GetCommitmentRates: layer %s: unusable rate %v", layer, rate)
	}
	return nil
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
		if err := checkUsableRate(l.layer, rate); err != nil {
			return nil, err
		}
		rates[l.layer] = rate
	}
	return rates, nil
}
