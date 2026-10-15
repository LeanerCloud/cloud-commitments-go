package ladder

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type spCommitmentRateSource interface {
	GetSPCommitmentRate(context.Context, recommendations.SPCommitmentRateParams) (float64, error)
}

// GetCommitmentRates leaves Compute absent pending the caller's sizing policy;
// regional EC2 Instance recommendations are not a Compute pricing proxy.
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
	rate, err := a.spRates.GetSPCommitmentRate(ctx, recommendations.SPCommitmentRateParams{
		PlanType:  types.SupportedSavingsPlansTypeEc2InstanceSp,
		AccountID: a.cfg.AccountID, Region: a.cfg.Region,
		Term: string(term), PaymentOption: string(payment),
		LookbackPeriod: fmt.Sprintf("%dd", a.cfg.lookbackDays()),
	})
	if errors.Is(err, recommendations.ErrNoSPCommitmentRate) {
		return map[ladder.LayerType]float64{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetCommitmentRates: %w", err)
	}
	return map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: rate}, nil
}
