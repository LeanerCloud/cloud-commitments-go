package recommendations

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// RIUtilization is an alias for common.RIUtilization so existing callers that
// import this package continue to compile without changes. The canonical
// definition lives in pkg/common so the Azure provider can return the same
// type without a cross-provider dependency.
type RIUtilization = common.RIUtilization

// riAccumulator aggregates utilization hours across multiple daily entries for one RI.
type riAccumulator struct {
	purchasedHours   float64
	totalActualHours float64
	unusedHours      float64
}

// GetRIUtilization fetches per-RI utilization from Cost Explorer for the
// last N days, scoped to EC2 RIs in region.
//
// Without a Filter, GetReservationUtilization blends utilization across
// every reserved-resource type in the account (RDS, ElastiCache,
// OpenSearch, Redshift, standard EC2 RIs) and every region into one
// SUBSCRIPTION_ID-grouped number. Callers that read this as "EC2
// convertible-RI utilization for this region" (e.g. the ladder
// ConvertibleRI layer) would then trigger real reshape/exchange
// decisions off an unrelated RI's utilization (PR #1361). region
// is optional: an empty string omits the REGION dimension, matching
// callers that haven't resolved a specific region.
func (c *Client) GetRIUtilization(ctx context.Context, lookbackDays int, region string) ([]RIUtilization, error) {
	if lookbackDays <= 0 {
		lookbackDays = 30
	}

	end := time.Now().UTC()
	start := end.AddDate(0, 0, -lookbackDays)

	input := &costexplorer.GetReservationUtilizationInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		GroupBy: []types.GroupDefinition{
			{
				Type: types.GroupDefinitionTypeDimension,
				Key:  aws.String("SUBSCRIPTION_ID"),
			},
		},
		Filter: ec2UtilizationFilter(region),
	}

	agg := make(map[string]*riAccumulator)
	page := 0

	err := paginateCE(ctx, maxUtilizationPages, "utilization", func(token *string) (*string, error) {
		input.NextPageToken = token
		result, err := c.fetchUtilizationPage(ctx, input)
		if err != nil {
			return nil, err
		}
		page++
		for _, group := range result.UtilizationsByTime {
			for _, detail := range group.Groups {
				if err := aggregateUtilizationDetail(agg, detail); err != nil {
					return nil, fmt.Errorf("utilization page %d: %w", page, err)
				}
			}
		}
		return result.NextPageToken, nil
	})
	if err != nil {
		return nil, err
	}

	return buildUtilizations(agg), nil
}

// ec2UtilizationFilter builds the CE Filter expression scoping
// GetReservationUtilization to EC2 RIs, optionally narrowed to one
// region. Mirrors serviceRegionFilter in coverage.go so both CE
// query paths agree on how a (service, region) scope is expressed.
func ec2UtilizationFilter(region string) *types.Expression {
	svc := types.Expression{Dimensions: &types.DimensionValues{Key: types.DimensionService, Values: []string{ec2ComputeService}}}
	if region == "" {
		return &svc
	}
	return &types.Expression{
		And: []types.Expression{
			svc,
			{Dimensions: &types.DimensionValues{Key: types.DimensionRegion, Values: []string{region}}},
		},
	}
}

func buildUtilizations(agg map[string]*riAccumulator) []RIUtilization {
	utilizations := make([]RIUtilization, 0, len(agg))
	for id, a := range agg {
		pct := 0.0
		if a.purchasedHours > 0 {
			pct = (a.totalActualHours / a.purchasedHours) * 100
		}
		utilizations = append(utilizations, RIUtilization{
			ReservedInstanceID: id,
			UtilizationPercent: pct,
			PurchasedHours:     a.purchasedHours,
			TotalActualHours:   a.totalActualHours,
			UnusedHours:        a.unusedHours,
		})
	}
	return utilizations
}

// fetchUtilizationPage calls the Cost Explorer API with rate-limit retry.
// The shared concurrency semaphore (CUDLY_MAX_PARALLELISM) is held only
// across the outbound SDK call, matching fetchRIPageWithRetry, so callers
// running under a semaphore-carrying context stay inside the global IO cap.
func (c *Client) fetchUtilizationPage(ctx context.Context, input *costexplorer.GetReservationUtilizationInput) (*costexplorer.GetReservationUtilizationOutput, error) {
	return fetchWithRetry(ctx, c.rateLimiter, "failed to get reservation utilization", func() (*costexplorer.GetReservationUtilizationOutput, error) {
		return c.costExplorerClient.GetReservationUtilization(ctx, input)
	})
}

// aggregateUtilizationDetail accumulates hours from a single utilization detail
// into the per-RI aggregation map. A detail with no Utilization, or with all
// three hours fields omitted, carries no usage signal and is skipped without
// creating an accumulator. Otherwise all three fields are required: a missing
// one would read as 0 hours and fake a utilization percent. A present but
// unparseable, non-finite or negative value is an error for the same reason.
func aggregateUtilizationDetail(agg map[string]*riAccumulator, detail types.ReservationUtilizationGroup) error {
	if detail.Key == nil || detail.Utilization == nil {
		return nil
	}
	id := aws.ToString(detail.Key)
	u := detail.Utilization
	if u.PurchasedHours == nil && u.TotalActualHours == nil && u.UnusedHours == nil {
		return nil
	}
	purchased, actual, unused, err := parseUtilizationHours(u)
	if err != nil {
		return fmt.Errorf("reservation %s: %w", id, err)
	}

	a, ok := agg[id]
	if !ok {
		a = &riAccumulator{}
		agg[id] = a
	}
	a.purchasedHours += purchased
	a.totalActualHours += actual
	a.unusedHours += unused
	return nil
}

// parseUtilizationHours strictly parses the three required hours fields.
func parseUtilizationHours(u *types.ReservationAggregates) (purchased, actual, unused float64, err error) {
	for _, f := range []struct {
		src  *string
		dst  *float64
		name string
	}{
		{src: u.PurchasedHours, dst: &purchased, name: "PurchasedHours"},
		{src: u.TotalActualHours, dst: &actual, name: "TotalActualHours"},
		{src: u.UnusedHours, dst: &unused, name: "UnusedHours"},
	} {
		if f.src == nil {
			return 0, 0, 0, fmt.Errorf("%s is missing while other utilization hours are present", f.name)
		}
		if *f.dst, err = parseOptionalFloat(f.name, f.src); err != nil {
			return 0, 0, 0, err
		}
	}
	return purchased, actual, unused, nil
}
