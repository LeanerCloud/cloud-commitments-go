package recommendations

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// noInstanceTypeKey is the CE INSTANCE_TYPE group key for on-demand spend that
// carries no instance type (for example, some data transfer or support rows).
// It belongs to no instance family, so the family series skips it explicitly.
const noInstanceTypeKey = "NoInstanceType"

// GetOnDemandFamilySeriesForAccount returns the daily on-demand EC2 cost per
// instance family (USD/hr) for one usage account over the lookback window. It
// issues one DAILY GetCostAndUsage grouped by INSTANCE_TYPE; the family is the
// part of the instance type before the first ".". Days a family has no spend
// are zero-filled, but only for days CE returns at all, so CE reporting lag does
// not zero every family. An empty result returns an empty map, not an error:
// the caller falls back to the non-family series.
func (c *Client) GetOnDemandFamilySeriesForAccount(ctx context.Context, accountID, region string, lookbackDays int) (map[string][]DailyCost, error) {
	if accountID == "" {
		return nil, fmt.Errorf("GetOnDemandFamilySeriesForAccount: accountID must not be empty")
	}
	if err := validateOnDemandSeriesArgs(region, lookbackDays); err != nil {
		return nil, err
	}

	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -lookbackDays)

	input := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start.Format(ceDateLayout)),
			End:   aws.String(end.Format(ceDateLayout)),
		},
		Granularity: types.GranularityDaily,
		Metrics:     []string{onDemandMetric},
		Filter:      onDemandSeriesFilter(region),
		GroupBy: []types.GroupDefinition{{
			Type: types.GroupDefinitionTypeDimension,
			Key:  aws.String(string(types.DimensionInstanceType)),
		}},
	}
	input.Filter.And = append(input.Filter.And, types.Expression{Dimensions: &types.DimensionValues{
		Key: types.DimensionLinkedAccount, Values: []string{accountID},
	}})

	spend := make(map[string]map[string]float64) // family -> day -> USD
	days := make(map[string]bool)

	err := paginateCE(ctx, maxOnDemandSeriesPages, "GetOnDemandFamilySeries", func(token *string) (*string, error) {
		input.NextPageToken = token
		out, err := c.fetchOnDemandPage(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("GetOnDemandFamilySeries: %w", err)
		}
		if err := accumulateFamilyResults(spend, days, out, start, end); err != nil {
			return nil, fmt.Errorf("GetOnDemandFamilySeries: %w", err)
		}
		return out.NextPageToken, nil
	})
	if err != nil {
		return nil, err
	}
	return buildFamilySeries(spend, days)
}

// accumulateFamilyResults merges one page of grouped daily results into spend.
// A day can span pages and a family can span several instance types, so values
// are summed, not overwritten. Each row must be one day inside the window and
// each group must carry exactly one INSTANCE_TYPE key. Unlike the flat path,
// duplicate day periods are allowed here because pages may split one day.
func accumulateFamilyResults(spend map[string]map[string]float64, days map[string]bool, out *costexplorer.GetCostAndUsageOutput, start, end time.Time) error {
	for _, row := range out.ResultsByTime {
		dayKey, err := familyRowDay(row, start, end)
		if err != nil {
			return err
		}
		days[dayKey] = true
		for _, g := range row.Groups {
			if err := accumulateFamilyGroup(spend, dayKey, g); err != nil {
				return err
			}
		}
	}
	return nil
}

// familyRowDay validates that a grouped row covers exactly one day inside the
// window and returns that day as YYYY-MM-DD.
func familyRowDay(row types.ResultByTime, start, end time.Time) (string, error) {
	if row.TimePeriod == nil || row.TimePeriod.Start == nil || row.TimePeriod.End == nil {
		return "", fmt.Errorf("CE family result is missing its time period")
	}
	day, err := time.Parse(ceDateLayout, aws.ToString(row.TimePeriod.Start))
	if err != nil {
		return "", fmt.Errorf("CE family period start: %w", err)
	}
	next, err := time.Parse(ceDateLayout, aws.ToString(row.TimePeriod.End))
	if err != nil {
		return "", fmt.Errorf("CE family period end: %w", err)
	}
	if !next.Equal(day.AddDate(0, 0, 1)) || day.Before(start) || next.After(end) {
		return "", fmt.Errorf("CE family period %s to %s is not one day inside the requested window", day, next)
	}
	return day.Format(ceDateLayout), nil
}

// accumulateFamilyGroup adds one INSTANCE_TYPE group's USD/hr to its family.
// NoInstanceType rows are skipped by design; any other key must have a family.
func accumulateFamilyGroup(spend map[string]map[string]float64, dayKey string, g types.Group) error {
	if len(g.Keys) != 1 {
		return fmt.Errorf("CE family group on %s has %d keys, want 1", dayKey, len(g.Keys))
	}
	key := g.Keys[0]
	if key == noInstanceTypeKey {
		return nil
	}
	family, _, ok := strings.Cut(key, ".")
	if !ok || family == "" {
		return fmt.Errorf("CE instance type %q on %s has no family prefix", key, dayKey)
	}
	usd, err := familyGroupUSD(g, key, dayKey)
	if err != nil {
		return err
	}
	if spend[family] == nil {
		spend[family] = make(map[string]float64)
	}
	// Divide daily USD by 24 to get USD/hr (DAILY granularity: 1 day = 24 hours).
	spend[family][dayKey] += usd / 24.0
	return nil
}

// familyGroupUSD reads the finite UnblendedCost amount of one group. A missing
// metric or amount is an absent observation, not zero, so it fails loud.
func familyGroupUSD(g types.Group, key, dayKey string) (float64, error) {
	mv, ok := g.Metrics[onDemandMetric]
	if !ok || mv.Amount == nil {
		return 0, fmt.Errorf("CE family group %q on %s is missing the %q metric or amount", key, dayKey, onDemandMetric)
	}
	usd, err := strconv.ParseFloat(aws.ToString(mv.Amount), 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse CE amount %q for %s on %s: %w", aws.ToString(mv.Amount), key, dayKey, err)
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("CE amount %q for %s on %s is not a finite number", aws.ToString(mv.Amount), key, dayKey)
	}
	return usd, nil
}

// buildFamilySeries returns one oldest-to-newest series per family over the
// days CE returned, zero-filling a family's missing days. An empty spend map
// returns an empty result (no family data), which the caller treats as the
// fallback case.
func buildFamilySeries(spend map[string]map[string]float64, days map[string]bool) (map[string][]DailyCost, error) {
	out := make(map[string][]DailyCost, len(spend))
	if len(spend) == 0 {
		return out, nil
	}
	ordered := make([]string, 0, len(days))
	for d := range days {
		ordered = append(ordered, d)
	}
	sort.Strings(ordered)
	for family, byDay := range spend {
		series := make([]DailyCost, len(ordered))
		for i, d := range ordered {
			t, err := time.Parse(ceDateLayout, d)
			if err != nil {
				return nil, fmt.Errorf("GetOnDemandFamilySeries: day %q does not parse: %w", d, err)
			}
			series[i] = DailyCost{Date: t, USDPerHour: byDay[d]}
		}
		out[family] = series
	}
	return out, nil
}
