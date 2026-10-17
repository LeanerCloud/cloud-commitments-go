package recommendations

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// usageHistoryLookbackDays is the number of daily samples fetched for the
// inline sparkline. 7 days is wide enough to show a weekly pattern without
// ballooning the payload; it matches the default LookbackPeriod used by
// GetRecommendationsForService.
const usageHistoryLookbackDays = 7

// GetDailyUsagePcts returns a slice of daily RI-coverage percentages (0-100)
// for the given (service, resourceType, region) over the last
// usageHistoryLookbackDays days, ordered oldest-to-newest. Each element
// corresponds to one calendar day. If CE returns no data for a day the slot
// is filled with 0.0 so the sparkline always has exactly
// usageHistoryLookbackDays points when data is present.
//
// Returns (nil, nil) on API success when CE has no historical data at all for
// the tuple; callers store nil so the frontend renders "—" rather than a
// flat-zero sparkline.
//
// serviceFilter is the canonical CE SERVICE dimension value (e.g.
// "Amazon Elastic Compute Cloud - Compute"). The resourceType is matched
// by the INSTANCE_TYPE dimension so we only pick up coverage for the
// exact SKU the recommendation targets.
func (c *Client) GetDailyUsagePcts(ctx context.Context, serviceFilter, resourceType, region string) ([]float64, error) {
	if serviceFilter == "" || resourceType == "" || region == "" {
		return nil, nil
	}

	end := time.Now().UTC()
	start := end.AddDate(0, 0, -usageHistoryLookbackDays)

	dayPct, start2 := newDayWindow(start)
	anyData, err := c.fetchDailyCoverage(ctx, serviceFilter, resourceType, region, start2, end, dayPct)
	if err != nil {
		return nil, err
	}
	if !anyData {
		return nil, nil
	}
	return orderedDaySlice(start2, dayPct), nil
}

// newDayWindow initializes a zeroed day map for the lookback window starting at
// start and returns both the map and start unchanged (to keep the call site
// readable).
func newDayWindow(start time.Time) (map[string]float64, time.Time) {
	dayPct := make(map[string]float64, usageHistoryLookbackDays)
	for i := 0; i < usageHistoryLookbackDays; i++ {
		dayPct[start.AddDate(0, 0, i).Format("2006-01-02")] = 0.0
	}
	return dayPct, start
}

// orderedDaySlice converts dayPct into a slice ordered oldest-to-newest.
func orderedDaySlice(start time.Time, dayPct map[string]float64) []float64 {
	out := make([]float64, usageHistoryLookbackDays)
	for i := 0; i < usageHistoryLookbackDays; i++ {
		out[i] = dayPct[start.AddDate(0, 0, i).Format("2006-01-02")]
	}
	return out
}

// fetchDailyCoverage pages through GetReservationCoverage and applies matching
// results into dayPct. Returns true if any data was written.
func (c *Client) fetchDailyCoverage(ctx context.Context, serviceFilter, resourceType, region string, start, end time.Time, dayPct map[string]float64) (bool, error) {
	input := &costexplorer.GetReservationCoverageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		Granularity: types.GranularityDaily,
		Filter:      dailyUsageFilter(serviceFilter, resourceType, region),
		Metrics:     []string{"Hour"},
	}

	anyData := false
	page := 0
	err := paginateCE(ctx, maxRecommendationPages, "GetReservationCoverage", func(token *string) (*string, error) {
		input.NextPageToken = token
		result, err := c.fetchCoveragePage(ctx, input)
		if err != nil {
			return nil, err
		}
		page++
		wrote, err := applyPeriodsToDayMap(result.CoveragesByTime, dayPct)
		if err != nil {
			return nil, fmt.Errorf("coverage page %d: %w", page, err)
		}
		if wrote {
			anyData = true
		}
		return result.NextPageToken, nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to get daily coverage for %s/%s/%s: %w", serviceFilter, resourceType, region, err)
	}
	return anyData, nil
}

// applyPeriodsToDayMap writes each period's coverage percentage into dayPct.
// A period with no percentage (nil) is skipped as "no data"; a present but
// malformed one is an error rather than a fabricated 0% day.
func applyPeriodsToDayMap(periods []types.CoverageByTime, dayPct map[string]float64) (bool, error) {
	anyData := false
	for _, period := range periods {
		if period.TimePeriod == nil || period.TimePeriod.Start == nil {
			continue
		}
		day := aws.ToString(period.TimePeriod.Start)
		if period.Total == nil || period.Total.CoverageHours == nil ||
			period.Total.CoverageHours.CoverageHoursPercentage == nil {
			continue
		}
		pct, err := parseOptionalFloat("CoverageHoursPercentage", period.Total.CoverageHours.CoverageHoursPercentage)
		if err != nil {
			return false, fmt.Errorf("day %s: %w", day, err)
		}
		dayPct[day] = pct
		anyData = true
	}
	return anyData, nil
}

// regionKey identifies one (CE service filter, region) pair. Every
// recommendation sharing a regionKey can be served by the same region-wide
// GetReservationCoverage query grouped by INSTANCE_TYPE.
type regionKey struct{ service, region string }

// regionBucket holds the distinct resource types requested for one regionKey
// (in first-seen order) and the recommendation indices for each type.
type regionBucket struct {
	key        regionKey
	types      []string
	recsByType map[string][]int
}

// groupRecsByRegion builds an ordered list of (service, region) buckets, each
// listing its distinct resource types and the recommendation indices that
// share each (service, region, resourceType) tuple.
func groupRecsByRegion(recs []common.Recommendation) []*regionBucket {
	buckets := make([]*regionBucket, 0)
	byKey := make(map[regionKey]*regionBucket)
	for i := range recs {
		r := &recs[i]
		sf := getServiceStringForCostExplorer(r.Service)
		if sf == "" || r.Region == "" || r.ResourceType == "" {
			continue
		}
		k := regionKey{sf, r.Region}
		b, ok := byKey[k]
		if !ok {
			b = &regionBucket{key: k, recsByType: make(map[string][]int)}
			byKey[k] = b
			buckets = append(buckets, b)
		}
		if _, seen := b.recsByType[r.ResourceType]; !seen {
			b.types = append(b.types, r.ResourceType)
		}
		b.recsByType[r.ResourceType] = append(b.recsByType[r.ResourceType], i)
	}
	return buckets
}

// AttachDailyUsageHistory enriches each recommendation in recs with a
// UsageHistory slice of daily RI-coverage percentages. Recs are batched by
// (serviceFilter, region) so the number of Cost Explorer requests scales with
// regions, not with distinct SKUs:
//
//   - A bucket with at most usageHistoryLookbackDays distinct resource types
//     issues one DAILY query per type (GetDailyUsagePcts).
//   - A larger bucket issues one region-wide query per day grouped by
//     INSTANCE_TYPE (getDailyUsagePctsByType) and fans the per-type series
//     out to every rec. CE rejects DAILY granularity combined with GroupBy,
//     so a single grouped request cannot return a daily series; one grouped
//     request per day is the smallest batch CE can express.
//
// Either way a bucket costs at most min(types, usageHistoryLookbackDays)
// requests (plus extra pages). Errors are logged and skipped so a single CE
// failure doesn't drop the whole collection; a failed fetch leaves
// UsageHistory nil rather than reporting zero coverage.
//
// SavingsPlans have no per-instance-type coverage breakdown in CE, so recs
// whose Service resolves to the empty coverage-filter string are silently
// skipped.
func (c *Client) AttachDailyUsageHistory(ctx context.Context, recs []common.Recommendation) {
	for _, b := range groupRecsByRegion(recs) {
		if ctx.Err() != nil {
			return
		}
		if len(b.types) > usageHistoryLookbackDays {
			c.attachBucketGrouped(ctx, recs, b)
			continue
		}
		c.attachBucketPerType(ctx, recs, b)
	}
}

// attachBucketPerType fetches one DAILY coverage series per resource type.
func (c *Client) attachBucketPerType(ctx context.Context, recs []common.Recommendation, b *regionBucket) {
	for _, rt := range b.types {
		if ctx.Err() != nil {
			return
		}
		pcts, err := c.GetDailyUsagePcts(ctx, b.key.service, rt, b.key.region)
		if err != nil {
			logging.Warnf("usage_history: failed to fetch daily coverage for %s/%s/%s: %v", b.key.service, rt, b.key.region, err)
			continue
		}
		// pcts==nil means no CE data for this tuple; leave UsageHistory nil.
		if pcts == nil {
			continue
		}
		for _, idx := range b.recsByType[rt] {
			recs[idx].UsageHistory = pcts
		}
	}
}

// attachBucketGrouped fetches the whole bucket with per-day grouped queries.
// Any failure leaves every rec in the bucket without UsageHistory, since a
// partial window would otherwise show the failed days as 0% coverage.
func (c *Client) attachBucketGrouped(ctx context.Context, recs []common.Recommendation, b *regionBucket) {
	series, err := c.getDailyUsagePctsByType(ctx, b.key.service, b.key.region, b.types)
	if err != nil {
		logging.Warnf("usage_history: failed to fetch daily coverage for %s/%s (%d instance types): %v", b.key.service, b.key.region, len(b.types), err)
		return
	}
	for _, rt := range b.types {
		pcts := series[rt]
		if pcts == nil {
			continue
		}
		for _, idx := range b.recsByType[rt] {
			recs[idx].UsageHistory = pcts
		}
	}
}

// getDailyUsagePctsByType returns, for each of resourceTypes, the same
// series GetDailyUsagePcts would return for that type: usageHistoryLookbackDays
// points ordered oldest-to-newest, with 0 for days CE omits, and no entry
// (nil) for a type CE has no data for at all. It issues one region-wide
// GetReservationCoverage request per day (plus pages) grouped by
// INSTANCE_TYPE, because CE does not accept DAILY granularity with GroupBy.
func (c *Client) getDailyUsagePctsByType(ctx context.Context, serviceFilter, region string, resourceTypes []string) (map[string][]float64, error) {
	if serviceFilter == "" || region == "" || len(resourceTypes) == 0 {
		return nil, nil
	}
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -usageHistoryLookbackDays)

	// CE reports instanceType attributes lower-cased by extractGroupAttributes;
	// index the requested types the same way.
	acc := groupedDayAccumulator{dayPct: make(map[string]map[string]float64), byLower: make(map[string][]string), withData: make(map[string]bool)}
	for _, rt := range resourceTypes {
		acc.dayPct[rt], _ = newDayWindow(start)
		lower := strings.ToLower(rt)
		acc.byLower[lower] = append(acc.byLower[lower], rt)
	}
	for i := 0; i < usageHistoryLookbackDays; i++ {
		if err := c.fetchGroupedDayCoverage(ctx, serviceFilter, region, start.AddDate(0, 0, i), &acc); err != nil {
			return nil, err
		}
	}
	out := make(map[string][]float64, len(acc.withData))
	for rt := range acc.withData {
		out[rt] = orderedDaySlice(start, acc.dayPct[rt])
	}
	return out, nil
}

// groupedDayAccumulator collects per-type daily percentages from grouped
// coverage responses.
type groupedDayAccumulator struct {
	dayPct   map[string]map[string]float64 // resourceType -> day -> pct
	byLower  map[string][]string           // lower-cased type -> requested types
	withData map[string]bool               // types with at least one data point
}

// fetchGroupedDayCoverage pages through one region-wide, single-day
// GetReservationCoverage request grouped by INSTANCE_TYPE.
func (c *Client) fetchGroupedDayCoverage(ctx context.Context, serviceFilter, region string, day time.Time, acc *groupedDayAccumulator) error {
	input := &costexplorer.GetReservationCoverageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(day.Format("2006-01-02")),
			End:   aws.String(day.AddDate(0, 0, 1).Format("2006-01-02")),
		},
		GroupBy: []types.GroupDefinition{
			{Type: types.GroupDefinitionTypeDimension, Key: aws.String(string(types.DimensionInstanceType))},
		},
		Filter:  serviceRegionFilter(serviceFilter, region),
		Metrics: []string{"Hour"},
	}
	page := 0
	return paginateCE(ctx, maxCoveragePages, "grouped daily coverage", func(token *string) (*string, error) {
		input.NextPageToken = token
		result, err := c.fetchCoveragePage(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to get daily coverage for %s/%s on %s: %w", serviceFilter, region, day.Format("2006-01-02"), err)
		}
		page++
		if err := acc.addPeriods(result.CoveragesByTime); err != nil {
			return nil, fmt.Errorf("grouped daily coverage for %s/%s on %s page %d: %w", serviceFilter, region, day.Format("2006-01-02"), page, err)
		}
		// Fail fast on a token that doesn't advance; paginateCE's cap catches
		// longer cycles and endless fresh tokens.
		if next := aws.ToString(result.NextPageToken); next != "" && next == aws.ToString(token) {
			return nil, fmt.Errorf("failed to get daily coverage for %s/%s on %s: repeated page token", serviceFilter, region, day.Format("2006-01-02"))
		}
		return result.NextPageToken, nil
	})
}

// addPeriods applies each group's coverage percentage to the requested
// types it matches, skipping periods without a start date and groups without
// a CoverageHoursPercentage, exactly as applyPeriodsToDayMap does for Totals.
// A present but malformed percentage is an error naming the day and type.
func (a *groupedDayAccumulator) addPeriods(periods []types.CoverageByTime) error {
	for _, period := range periods {
		if period.TimePeriod == nil || period.TimePeriod.Start == nil {
			continue
		}
		day := aws.ToString(period.TimePeriod.Start)
		for _, group := range period.Groups {
			instType, _ := extractGroupAttributes(group.Attributes)
			if len(a.byLower[instType]) == 0 {
				continue // region-wide response: ignore types nobody requested
			}
			pct, ok, err := groupCoveragePct(group)
			if err != nil {
				return fmt.Errorf("day %s instance type %q: %w", day, instType, err)
			}
			if !ok {
				continue
			}
			for _, rt := range a.byLower[instType] {
				a.dayPct[rt][day] = pct
				a.withData[rt] = true
			}
		}
	}
	return nil
}

// groupCoveragePct returns the group's parsed CoverageHoursPercentage, or
// false when CE omitted it. A present but malformed value is an error.
func groupCoveragePct(group types.ReservationCoverageGroup) (pct float64, ok bool, err error) {
	if group.Coverage == nil || group.Coverage.CoverageHours == nil ||
		group.Coverage.CoverageHours.CoverageHoursPercentage == nil {
		return 0, false, nil
	}
	pct, err = parseOptionalFloat("CoverageHoursPercentage", group.Coverage.CoverageHours.CoverageHoursPercentage)
	return pct, err == nil, err
}

func dailyUsageFilter(serviceFilter, resourceType, region string) *types.Expression {
	filter := serviceRegionFilter(serviceFilter, region)
	filter.And = append(filter.And, types.Expression{Dimensions: &types.DimensionValues{
		Key: types.DimensionInstanceType, Values: []string{resourceType},
	}})
	return filter
}
