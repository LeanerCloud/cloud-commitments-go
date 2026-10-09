package recommendations

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func coverageGroup(instance, deployment, pct string, hours *string) types.ReservationCoverageGroup {
	return types.ReservationCoverageGroup{
		Attributes: map[string]string{"instanceType": instance, "deploymentOption": deployment},
		Coverage: &types.Coverage{CoverageHours: &types.CoverageHours{
			CoverageHoursPercentage: aws.String(pct), TotalRunningHours: hours,
		}},
	}
}

func TestGetRICoverageMap_AggregatesBucketsSDK(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		paged, reverse, failLast bool
	}{
		{name: "one page"}, {name: "multiple pages", paged: true},
		{name: "reversed", paged: true, reverse: true}, {name: "late error", paged: true, failLast: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input costexplorer.GetReservationCoverageInput
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Empty(t, input.Granularity)
				assert.Equal(t, "Hour", input.Metrics[0])
				var service, engine, region string
				for _, term := range input.Filter.And {
					switch term.Dimensions.Key {
					case types.DimensionService:
						service = term.Dimensions.Values[0]
					case types.DimensionDatabaseEngine:
						engine = term.Dimensions.Values[0]
					case types.DimensionRegion:
						region = term.Dimensions.Values[0]
					}
				}
				out := costexplorer.GetReservationCoverageOutput{}
				if service == coverageServiceFilters[0] || engine == "MySQL" || engine == "PostgreSQL" {
					assert.Equal(t, "INSTANCE_TYPE", aws.ToString(input.GroupBy[0].Key))
					start, err := time.Parse(time.DateOnly, aws.ToString(input.TimePeriod.Start))
					if !assert.NoError(t, err) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					end, err := time.Parse(time.DateOnly, aws.ToString(input.TimePeriod.End))
					if !assert.NoError(t, err) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, 30*24*time.Hour, end.Sub(start))
					instance, deployment := "m5.large", ""
					if engine != "" {
						instance, deployment = "db.m5.large", "Single-AZ"
					}
					periods := []types.CoverageByTime{
						{TimePeriod: &types.DateInterval{Start: input.TimePeriod.Start, End: aws.String(end.AddDate(0, 0, -2).Format(time.DateOnly))}, Groups: []types.ReservationCoverageGroup{coverageGroup(instance, deployment, "75", aws.String("26880"))}},
						{TimePeriod: &types.DateInterval{Start: aws.String(end.AddDate(0, 0, -2).Format(time.DateOnly)), End: input.TimePeriod.End}, Groups: []types.ReservationCoverageGroup{coverageGroup(instance, normaliseDeployment(deployment), "25", aws.String("1920"))}},
					}
					if engine != "" {
						periods[0].Groups = append(periods[0].Groups, coverageGroup(instance, "Multi-AZ", "10", aws.String("720")))
					} else {
						periods[0].Groups = append(periods[0].Groups, coverageGroup("m5.xlarge", "", "20", aws.String("7200")))
					}
					if engine == "PostgreSQL" || region == "eu-west-1" {
						periods = periods[:1]
					}
					if tc.reverse {
						slices.Reverse(periods)
					}
					out.CoveragesByTime = periods
					if tc.paged {
						if input.NextPageToken == nil {
							out.CoveragesByTime = periods[:1]
							out.NextPageToken = aws.String("second")
						} else {
							assert.Equal(t, "second", *input.NextPageToken)
							if tc.failLast {
								w.WriteHeader(http.StatusBadRequest)
								_, _ = w.Write([]byte(`{"__type":"ValidationException","message":"late failure"}`))
								return
							}
							out.CoveragesByTime = periods[1:]
						}
					}
				}
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				assert.NoError(t, json.NewEncoder(w).Encode(out))
			}))
			defer server.Close()
			transport := server.Client().Transport.(*http.Transport).Clone()
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != server.Listener.Addr().String() {
					return nil, fmt.Errorf("unexpected SDK destination %q", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			defer transport.CloseIdleConnections()
			sdk := costexplorer.NewFromConfig(aws.Config{Region: "us-east-1", BaseEndpoint: aws.String(server.URL), Credentials: aws.AnonymousCredentials{}, HTTPClient: &http.Client{Transport: transport}})
			got, err := NewClientWithAPI(sdk, "us-east-1").GetRICoverageMap(context.Background(), 30, []string{"us-east-1", "eu-west-1", "us-east-1"})
			if tc.failLast {
				require.ErrorContains(t, err, "late failure")
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			for _, key := range []string{"us-east-1:m5.large", "us-east-1:db.m5.large:mysql:singleaz"} {
				assert.InDelta(t, 40, got[key].AvgInstancesPerHour, 1e-9, key)
				assert.InDelta(t, 215.0/3, got[key].Pct, 1e-9, key)
			}
			assert.Equal(t, PoolCoverage{Pct: 20, AvgInstancesPerHour: 10}, got["us-east-1:m5.xlarge"])
			assert.Equal(t, PoolCoverage{Pct: 10, AvgInstancesPerHour: 1}, got["us-east-1:db.m5.large:mysql:multiaz"])
			assert.Equal(t, PoolCoverage{Pct: 75, AvgInstancesPerHour: 112.0 / 3}, got["us-east-1:db.m5.large:postgresql:singleaz"])
			assert.Equal(t, PoolCoverage{Pct: 75, AvgInstancesPerHour: 112.0 / 3}, got["eu-west-1:m5.large"])
			recs := []common.Recommendation{{Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: 10}, {Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: 10}}
			ApplyCoverageMapToRecommendations(recs, got)
			assert.InDelta(t, 40, recs[0].AverageInstancesUsedPerHour+recs[1].AverageInstancesUsedPerHour, 1e-9)
			assert.InDelta(t, 215.0/3, recs[0].ExistingCoveragePct, 1e-9)
			assert.True(t, recs[0].ExistingCoverageKnown)
		})
	}
}

func TestGetRICoverageMap_ZeroWeightBuckets(t *testing.T) {
	for _, hours := range []*string{nil, aws.String("0")} {
		for _, reverse := range []bool{false, true} {
			groups := []types.ReservationCoverageGroup{
				coverageGroup("m5.large", "", "50", aws.String("720")),
				coverageGroup("m5.large", "", "99", hours),
				{Attributes: map[string]string{"instanceType": "m5.large"}},
			}
			if reverse {
				slices.Reverse(groups)
			}
			mock := &mockCoverageCE{coverageOutput: &costexplorer.GetReservationCoverageOutput{CoveragesByTime: []types.CoverageByTime{{Groups: groups}}}}
			got, err := NewClientWithAPI(mock, "us-east-1").GetRICoverageMap(context.Background(), 30, []string{"us-east-1"})
			require.NoError(t, err)
			assert.Equal(t, PoolCoverage{Pct: 50, AvgInstancesPerHour: 1}, got["us-east-1:m5.large"])
		}
	}
	groups := []types.ReservationCoverageGroup{coverageGroup("m5.large", "", "50", nil), coverageGroup("m5.large", "", "99", aws.String("0"))}
	mock := &mockCoverageCE{coverageOutput: &costexplorer.GetReservationCoverageOutput{CoveragesByTime: []types.CoverageByTime{{Groups: groups}}}}
	got, err := NewClientWithAPI(mock, "us-east-1").GetRICoverageMap(context.Background(), 30, []string{"us-east-1"})
	require.NoError(t, err)
	assert.Equal(t, PoolCoverage{Pct: 99}, got["us-east-1:m5.large"])
}

// Issue go#308: an unparsable, non-finite or negative value must remove the
// whole pool from the map, never leave it as a known 0%.
func TestGetRICoverageMap_InvalidTotalRunningHoursDropsPool(t *testing.T) {
	for _, hours := range []string{"invalid", "NaN", "Inf", "-720"} {
		for _, reverse := range []bool{false, true} {
			groups := []types.ReservationCoverageGroup{
				coverageGroup("m5.large", "", "50", aws.String("720")),
				coverageGroup("m5.large", "", "99", aws.String(hours)),
				coverageGroup("m5.xlarge", "", "20", aws.String("720")),
			}
			if reverse {
				slices.Reverse(groups)
			}
			mock := &mockCoverageCE{coverageOutput: &costexplorer.GetReservationCoverageOutput{CoveragesByTime: []types.CoverageByTime{{Groups: groups}}}}
			got, err := NewClientWithAPI(mock, "us-east-1").GetRICoverageMap(context.Background(), 30, []string{"us-east-1"})
			require.NoError(t, err)
			assert.NotContains(t, got, "us-east-1:m5.large", hours)
			assert.Equal(t, PoolCoverage{Pct: 20, AvgInstancesPerHour: 1}, got["us-east-1:m5.xlarge"])
		}
	}
}

func TestGetRICoverageMap_BadPercentageInOnePeriodDropsWholePool(t *testing.T) {
	periods := []types.CoverageByTime{
		{Groups: []types.ReservationCoverageGroup{coverageGroup("m5.large", "", "50", aws.String("720")), coverageGroup("m5.xlarge", "", "20", aws.String("720"))}},
		{Groups: []types.ReservationCoverageGroup{coverageGroup("m5.large", "", "abc", aws.String("720"))}},
	}
	mock := &mockCoverageCE{coverageOutput: &costexplorer.GetReservationCoverageOutput{CoveragesByTime: periods}}
	got, err := NewClientWithAPI(mock, "us-east-1").GetRICoverageMap(context.Background(), 30, []string{"us-east-1"})
	require.NoError(t, err)
	assert.NotContains(t, got, "us-east-1:m5.large")
	assert.Equal(t, PoolCoverage{Pct: 20, AvgInstancesPerHour: 1}, got["us-east-1:m5.xlarge"])
	recs := []common.Recommendation{{Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: 10}}
	ApplyCoverageMapToRecommendations(recs, got)
	assert.False(t, recs[0].ExistingCoverageKnown, "go#308: bad value must not become a known 0%")
}

func TestPoolCoverageFromGroup_Parse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pct     *string
		hours   *string
		want    PoolCoverage
		ok, err bool
	}{
		{name: "nil percentage skipped silently"},
		{name: "empty percentage is invalid", pct: aws.String(""), err: true},
		{name: "zero percentage is present", pct: aws.String("0"), hours: aws.String("720"), want: PoolCoverage{AvgInstancesPerHour: 1}, ok: true},
		{name: "valid", pct: aws.String("75.5"), hours: aws.String("1440"), want: PoolCoverage{Pct: 75.5, AvgInstancesPerHour: 2}, ok: true},
		{name: "unparsable", pct: aws.String("abc"), err: true},
		{name: "NaN", pct: aws.String("NaN"), err: true},
		{name: "Inf", pct: aws.String("Inf"), err: true},
		{name: "negative", pct: aws.String("-5"), err: true},
		{name: "bad hours", pct: aws.String("50"), hours: aws.String("x"), err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var group types.ReservationCoverageGroup
			if tc.pct != nil {
				group.Coverage = &types.Coverage{CoverageHours: &types.CoverageHours{CoverageHoursPercentage: tc.pct, TotalRunningHours: tc.hours}}
			}
			got, ok, err := poolCoverageFromGroup(group, 720)
			assert.Equal(t, tc.err, err != nil)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
