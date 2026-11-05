package recommendations

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// GetSPUtilizationForAccount is GetSPUtilization restricted to one linked
// account. Under payer credentials the unscoped call blends every account.
func (c *Client) GetSPUtilizationForAccount(ctx context.Context, planType types.SupportedSavingsPlansType, accountID, region string, lookbackDays int) (SPUtilizationSummary, error) {
	if accountID == "" {
		return SPUtilizationSummary{}, errors.New("sp utilization: accountID must not be empty")
	}
	return c.getSPUtilization(ctx, planType, accountID, region, lookbackDays)
}

// spUtilizationFilterForAccount returns spUtilizationFilter unchanged for an
// empty accountID. Otherwise it builds a new And, because spUtilizationFilter
// returns a bare dimension when region is empty and cannot be appended to.
func spUtilizationFilterForAccount(planType types.SupportedSavingsPlansType, accountID, region string) (*types.Expression, error) {
	base, err := spUtilizationFilter(planType, region)
	if err != nil || accountID == "" {
		return base, err
	}
	and := []types.Expression{}
	if base.And != nil {
		and = append(and, base.And...)
	} else {
		and = append(and, *base)
	}
	and = append(and, types.Expression{Dimensions: &types.DimensionValues{Key: types.DimensionLinkedAccount, Values: []string{accountID}}})
	return &types.Expression{And: and}, nil
}
