package recommendations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// RIFeeUtilization is one subscription's reservation fee and usage, summed
// over the lookback window.
type RIFeeUtilization struct {
	ReservedInstanceID        string
	TotalAmortizedFee         float64
	OnDemandCostOfRIHoursUsed float64
	PurchasedHours            float64
	TotalActualHours          float64
}

// GetRIFeeUtilization fetches per-subscription fee and hours for EC2 RIs of
// one linked account (and region, when non-empty) over the last lookbackDays.
// A group with all four fields absent is skipped; a partial, unparseable,
// non-finite or negative set is an error.
//
// MANUAL VERIFICATION REQUIRED (mock-unprovable): the tests prove only the
// request shape. Whether LINKED_ACCOUNT matches the owner account or the usage
// account is not provable offline. Before trusting this under payer
// credentials, run GetReservationUtilization with LINKED_ACCOUNT=<id> under the payer
// credentials, repeat it under that account's own credentials, and compare.
func (c *Client) GetRIFeeUtilization(ctx context.Context, accountID, region string, lookbackDays int) ([]RIFeeUtilization, error) {
	if accountID == "" {
		return nil, errors.New("ri fee utilization: accountID must not be empty")
	}
	if lookbackDays <= 0 {
		return nil, fmt.Errorf("ri fee utilization: lookbackDays must be positive, got %d", lookbackDays)
	}
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -lookbackDays)
	input := &costexplorer.GetReservationUtilizationInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		GroupBy: []types.GroupDefinition{{Type: types.GroupDefinitionTypeDimension, Key: aws.String("SUBSCRIPTION_ID")}},
		Filter:  ec2UtilizationFilterForAccount(accountID, region),
	}
	agg := make(map[string]*RIFeeUtilization)
	err := paginateCE(ctx, maxUtilizationPages, "ri fee utilization", func(token *string) (*string, error) {
		input.NextPageToken = token
		result, err := c.fetchUtilizationPage(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, group := range result.UtilizationsByTime {
			for _, detail := range group.Groups {
				if err := addRIFeeDetail(agg, detail); err != nil {
					return nil, err
				}
			}
		}
		return result.NextPageToken, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]RIFeeUtilization, 0, len(agg))
	for _, u := range agg {
		out = append(out, *u)
	}
	return out, nil
}

// ec2UtilizationFilterForAccount always builds a new And: ec2UtilizationFilter
// returns a bare dimension when region is empty, which cannot be appended to.
func ec2UtilizationFilterForAccount(accountID, region string) *types.Expression {
	and := []types.Expression{{Dimensions: &types.DimensionValues{Key: types.DimensionService, Values: []string{ec2ComputeService}}}}
	if region != "" {
		and = append(and, types.Expression{Dimensions: &types.DimensionValues{Key: types.DimensionRegion, Values: []string{region}}})
	}
	and = append(and, types.Expression{Dimensions: &types.DimensionValues{Key: types.DimensionLinkedAccount, Values: []string{accountID}}})
	return &types.Expression{And: and}
}

func addRIFeeDetail(agg map[string]*RIFeeUtilization, detail types.ReservationUtilizationGroup) error {
	if detail.Key == nil || detail.Utilization == nil {
		return nil
	}
	id := aws.ToString(detail.Key)
	u := detail.Utilization
	if u.TotalAmortizedFee == nil && u.OnDemandCostOfRIHoursUsed == nil && u.PurchasedHours == nil && u.TotalActualHours == nil {
		return nil
	}
	vals, err := parseRIFeeFields(id, u)
	if err != nil {
		return err
	}
	a, ok := agg[id]
	if !ok {
		a = &RIFeeUtilization{ReservedInstanceID: id}
		agg[id] = a
	}
	a.TotalAmortizedFee += vals[0]
	a.OnDemandCostOfRIHoursUsed += vals[1]
	a.PurchasedHours += vals[2]
	a.TotalActualHours += vals[3]
	return nil
}

func parseRIFeeFields(id string, u *types.ReservationAggregates) ([4]float64, error) {
	var vals [4]float64
	for i, f := range []struct {
		src  *string
		name string
	}{
		{u.TotalAmortizedFee, "TotalAmortizedFee"},
		{u.OnDemandCostOfRIHoursUsed, "OnDemandCostOfRIHoursUsed"},
		{u.PurchasedHours, "PurchasedHours"},
		{u.TotalActualHours, "TotalActualHours"},
	} {
		if f.src == nil {
			return vals, fmt.Errorf("reservation %s: %s is missing while other fee fields are present", id, f.name)
		}
		v, err := parseOptionalFloat(f.name, f.src)
		if err != nil {
			return vals, fmt.Errorf("reservation %s: %w", id, err)
		}
		vals[i] = v
	}
	return vals, nil
}
