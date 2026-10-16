package recommendations

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// spCoverageServiceEC2 is the SERVICE dimension value used to scope
// GetSavingsPlansCoverage to EC2 compute, matching the EC2-only on-demand
// residual series so covered and uncovered demand describe the same usage.
//
// MANUAL VERIFICATION REQUIRED (mock-unprovable): this string is verified
// offline only for the GetCostAndUsage and RI-coverage filters
// (ec2ComputeService). Whether GetSavingsPlansCoverage accepts the same
// vocabulary has not been confirmed against a live CE response; a wrong value
// makes CE match nothing (complete $0 rows, no error). Keep it a single named
// constant so a correction is one line.
const spCoverageServiceEC2 = "Amazon Elastic Compute Cloud - Compute"

// spCoverageSeriesFilter builds the GetSavingsPlansCoverage Filter for the
// covered-demand series: SERVICE = EC2 compute AND REGION = region. Both are
// supported coverage dimensions (see spCoverageRegionFilter). region is
// required: the covered series is paired with the region-scoped residual.
func spCoverageSeriesFilter(region string) *types.Expression {
	return &types.Expression{
		And: []types.Expression{
			{Dimensions: &types.DimensionValues{
				Key:    types.DimensionService,
				Values: []string{spCoverageServiceEC2},
			}},
			{Dimensions: &types.DimensionValues{
				Key:    types.DimensionRegion,
				Values: []string{region},
			}},
		},
	}
}

// GetSPCoveredSeries returns the daily EC2 usage covered by Savings Plans in
// region over the past lookbackDays days, ordered oldest-to-newest with
// strictly increasing unique UTC days.
//
// Units: CE's SpendCoveredBySavingsPlans is ON-DEMAND-EQUIVALENT dollars (the
// price the covered usage would have cost at on-demand rates), the same unit
// as the uncovered on-demand residual from GetOnDemandSeries. Each day's total
// is divided by 24 (DAILY granularity) to give USD/hr. It is NOT the SP
// commitment consumed; convert commitment to this unit with
// SPUtilizationSummary.CommitmentToOnDemandRatio.
//
// A row whose Coverage block is nil counts as zero covered (precedent:
// spCoverageAccumulator.add; CE omits the block for days without activity).
// An empty response yields an empty series and no error: an account without
// Savings Plans legitimately has no covered usage, and the caller joins this
// with the residual series. A non-nil Coverage block without
// SpendCoveredBySavingsPlans, an undatable row, a duplicate date, or an
// unparseable amount fails loud.
func (c *Client) GetSPCoveredSeries(ctx context.Context, region string, lookbackDays int) ([]DailyCost, error) {
	if lookbackDays <= 0 {
		return nil, fmt.Errorf("sp covered series: lookbackDays must be > 0, got %d", lookbackDays)
	}
	if region == "" {
		return nil, fmt.Errorf("sp covered series: region must not be empty")
	}
	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -lookbackDays)
	input := &costexplorer.GetSavingsPlansCoverageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start.Format(ceDateLayout)),
			End:   aws.String(end.Format(ceDateLayout)),
		},
		Granularity: types.GranularityDaily,
		Metrics:     []string{spCoverageMetricSpendCovered},
		Filter:      spCoverageSeriesFilter(region),
	}

	byDate, err := c.fetchSPCoveredByDate(ctx, input)
	if err != nil {
		return nil, err
	}
	return sortedDailyCosts(byDate)
}

// fetchSPCoveredByDate pages through GetSavingsPlansCoverage and returns
// USD/hr keyed by "YYYY-MM-DD".
func (c *Client) fetchSPCoveredByDate(ctx context.Context, input *costexplorer.GetSavingsPlansCoverageInput) (map[string]float64, error) {
	byDate := make(map[string]float64)
	err := paginateCE(ctx, maxSPCoveragePages, "sp covered series", func(token *string) (*string, error) {
		input.NextToken = token
		result, err := c.fetchSPCoveragePage(ctx, input)
		if err != nil {
			return nil, err
		}
		if err := accumulateSPCoveredDays(byDate, result.SavingsPlansCoverages); err != nil {
			return nil, fmt.Errorf("sp covered series: %w", err)
		}
		return result.NextToken, nil
	})
	if err != nil {
		return nil, err
	}
	return byDate, nil
}

// sortedDailyCosts orders a date-keyed map oldest-to-newest (lexicographic
// order of "YYYY-MM-DD" is chronological; map keys are unique).
func sortedDailyCosts(byDate map[string]float64) ([]DailyCost, error) {
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	series := make([]DailyCost, len(dates))
	for i, d := range dates {
		day, err := time.Parse(ceDateLayout, d)
		if err != nil {
			return nil, fmt.Errorf("sp covered series: cannot parse CE period start date %q: %w", d, err)
		}
		series[i] = DailyCost{Date: day, USDPerHour: byDate[d]}
	}
	return series, nil
}

// accumulateSPCoveredDays merges one page of daily coverage rows into byDate
// (USD/hr per "YYYY-MM-DD"). See GetSPCoveredSeries for the fail-loud rules.
func accumulateSPCoveredDays(byDate map[string]float64, rows []types.SavingsPlansCoverage) error {
	for i, r := range rows {
		if r.TimePeriod == nil || r.TimePeriod.Start == nil {
			return fmt.Errorf("CE coverage row %d is missing its period start; an undatable row would hide a gap in the daily series", i)
		}
		date := aws.ToString(r.TimePeriod.Start)
		if _, dup := byDate[date]; dup {
			return fmt.Errorf("CE returned more than one coverage row for day %s", date)
		}
		var usd float64
		if r.Coverage != nil {
			if r.Coverage.SpendCoveredBySavingsPlans == nil {
				return fmt.Errorf("CE coverage row for day %s has no %s value; CE echoes the requested metric on every row", date, spCoverageMetricSpendCovered)
			}
			v, err := parseSPFloat(aws.ToString(r.Coverage.SpendCoveredBySavingsPlans), spCoverageMetricSpendCovered)
			if err != nil {
				return fmt.Errorf("day %s: %w", date, err)
			}
			usd = v
		}
		byDate[date] = usd / 24.0
	}
	return nil
}
