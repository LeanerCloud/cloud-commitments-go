package recommendations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestAttachDailyUsageHistory_SDKDailyCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mode string
		want []float64
	}{
		{name: "paginated daily totals", want: []float64{10, 0, 30, 0, 0, 0, 70}},
		{name: "no data", mode: "empty"},
		{name: "missing total", mode: "missing"},
		{name: "service error", mode: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			var firstWindow *types.DateInterval
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				var input costexplorer.GetReservationCoverageInput
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				valid := assert.Equal(t, "AWSInsightsIndexService.GetReservationCoverage", r.Header.Get("X-Amz-Target"))
				valid = assert.Equal(t, types.GranularityDaily, input.Granularity) && valid
				valid = assert.Empty(t, input.GroupBy) && valid
				valid = assert.Equal(t, []string{"Hour"}, input.Metrics) && valid
				valid = assert.Equal(t, &types.Expression{And: []types.Expression{
					{Dimensions: &types.DimensionValues{Key: types.DimensionService, Values: []string{"Amazon Elastic Compute Cloud - Compute"}}},
					{Dimensions: &types.DimensionValues{Key: types.DimensionRegion, Values: []string{"us-east-1"}}},
					{Dimensions: &types.DimensionValues{Key: types.DimensionInstanceType, Values: []string{"m5.xlarge"}}},
				}}, input.Filter) && valid
				if !valid || input.TimePeriod == nil {
					http.Error(w, `{"__type":"ValidationException","message":"invalid coverage request"}`, http.StatusBadRequest)
					return
				}
				if call == 1 {
					firstWindow = input.TimePeriod
					assert.Empty(t, aws.ToString(input.NextPageToken))
				} else {
					assert.Equal(t, firstWindow, input.TimePeriod)
					assert.Equal(t, "page2", aws.ToString(input.NextPageToken))
				}
				start, err := time.Parse("2006-01-02", aws.ToString(input.TimePeriod.Start))
				if !assert.NoError(t, err) {
					http.Error(w, "invalid date", http.StatusBadRequest)
					return
				}
				assert.Equal(t, start.AddDate(0, 0, usageHistoryLookbackDays).Format("2006-01-02"), aws.ToString(input.TimePeriod.End))
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				if tc.mode == "error" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"__type":"DataUnavailableException","message":"no coverage available"}`))
					return
				}
				periods := []types.CoverageByTime{}
				var token *string
				if tc.mode != "empty" {
					days := []int{6, 0}
					if call == 1 && tc.mode == "" {
						token = aws.String("page2")
					} else if call == 2 {
						days = []int{2}
					}
					for _, day := range days {
						period := types.CoverageByTime{TimePeriod: &types.DateInterval{
							Start: aws.String(start.AddDate(0, 0, day).Format("2006-01-02")),
							End:   aws.String(start.AddDate(0, 0, day+1).Format("2006-01-02")),
						}}
						if tc.mode != "missing" {
							period.Total = &types.Coverage{CoverageHours: &types.CoverageHours{
								CoverageHoursPercentage: aws.String(fmt.Sprint((day + 1) * 10)),
							}}
						}
						periods = append(periods, period)
					}
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"CoveragesByTime": periods, "NextPageToken": token,
					"Total": &types.Coverage{CoverageHours: &types.CoverageHours{CoverageHoursPercentage: aws.String("99")}},
				}))
			}))
			defer server.Close()
			sdk := costexplorer.New(costexplorer.Options{
				Region: "us-east-1", BaseEndpoint: aws.String(server.URL),
				Credentials: aws.AnonymousCredentials{}, HTTPClient: server.Client(),
			})
			client := NewClientWithAPI(sdk, "us-east-1")
			rec := common.Recommendation{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.xlarge"}
			recs := []common.Recommendation{rec, rec}
			client.AttachDailyUsageHistory(context.Background(), recs)
			for _, r := range recs {
				assert.Equal(t, tc.want, r.UsageHistory)
			}
			wantCalls := int32(1)
			if tc.mode == "" {
				wantCalls = 2
			}
			assert.Equal(t, wantCalls, calls.Load())
		})
	}
}

func buildDailyOutput() *costexplorer.GetReservationCoverageOutput {
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -usageHistoryLookbackDays)
	periods := make([]types.CoverageByTime, 0, usageHistoryLookbackDays)
	for i := 0; i < usageHistoryLookbackDays; i++ {
		day := start.AddDate(0, 0, i)
		periods = append(periods, types.CoverageByTime{
			TimePeriod: &types.DateInterval{
				Start: aws.String(day.Format("2006-01-02")),
				End:   aws.String(day.AddDate(0, 0, 1).Format("2006-01-02")),
			},
			Total: &types.Coverage{
				CoverageHours: &types.CoverageHours{
					CoverageHoursPercentage: aws.String("80.0"),
				},
			},
		})
	}
	return &costexplorer.GetReservationCoverageOutput{CoveragesByTime: periods}
}

// TestGetDailyUsagePcts_ReturnsNDailyPoints asserts that GetDailyUsagePcts
// returns exactly usageHistoryLookbackDays points ordered oldest-to-newest
// when CE reports coverage for every day.
func TestGetDailyUsagePcts_ReturnsNDailyPoints(t *testing.T) {
	mock := &mockCoverageCE{
		coverageOutput: buildDailyOutput(),
	}

	client := NewClientWithAPI(mock, "us-east-1")
	pcts, err := client.GetDailyUsagePcts(context.Background(), "Amazon Elastic Compute Cloud - Compute", "m5.large", "us-east-1")

	require.NoError(t, err)
	require.NotNil(t, pcts, "expected non-nil slice when CE has data")
	assert.Len(t, pcts, usageHistoryLookbackDays, "should return exactly %d points", usageHistoryLookbackDays)
	for i, p := range pcts {
		assert.InDelta(t, 80.0, p, 0.001, "day %d: expected 80.0%% coverage", i)
	}
}

// TestGetDailyUsagePcts_ReturnsNilOnNoData asserts that GetDailyUsagePcts
// returns (nil, nil) when CE has no data for the tuple so the frontend
// renders "—" (not a flat-zero sparkline).
func TestGetDailyUsagePcts_ReturnsNilOnNoData(t *testing.T) {
	mock := &mockCoverageCE{
		coverageOutput: &costexplorer.GetReservationCoverageOutput{
			CoveragesByTime: []types.CoverageByTime{},
		},
	}

	client := NewClientWithAPI(mock, "us-east-1")
	pcts, err := client.GetDailyUsagePcts(context.Background(), "Amazon Elastic Compute Cloud - Compute", "m5.large", "us-east-1")

	require.NoError(t, err)
	assert.Nil(t, pcts, "nil means no data; frontend renders a dash, not a flat-zero sparkline")
}

// TestGetDailyUsagePcts_EmptyInputsReturnNil asserts that empty serviceFilter,
// resourceType, or region short-circuit without an API call.
func TestGetDailyUsagePcts_EmptyInputsReturnNil(t *testing.T) {
	mock := &mockCoverageCE{}

	client := NewClientWithAPI(mock, "us-east-1")

	pcts, err := client.GetDailyUsagePcts(context.Background(), "", "m5.large", "us-east-1")
	require.NoError(t, err)
	assert.Nil(t, pcts)
	assert.Equal(t, 0, mock.coverageCalls, "no CE call when serviceFilter is empty")
}

// TestAttachDailyUsageHistory_PopulatesUsageHistory is the end-to-end
// assertion required by issue #239: given a slice of recommendations with a
// single distinct (service, region, resourceType) tuple, AttachDailyUsageHistory
// should populate every matching rec's UsageHistory field with the daily
// coverage percentages returned by CE.
func TestAttachDailyUsageHistory_PopulatesUsageHistory(t *testing.T) {
	mock := &mockCoverageCE{
		coverageOutput: buildDailyOutput(),
	}

	client := NewClientWithAPI(mock, "us-east-1")
	recs := []common.Recommendation{
		{
			Service:      common.ServiceEC2,
			Region:       "us-east-1",
			ResourceType: "m5.xlarge",
		},
		{
			Service:      common.ServiceEC2,
			Region:       "us-east-1",
			ResourceType: "m5.xlarge",
		},
	}

	client.AttachDailyUsageHistory(context.Background(), recs)

	for i, r := range recs {
		require.NotNil(t, r.UsageHistory, "rec[%d] UsageHistory must be non-nil after attach", i)
		assert.Len(t, r.UsageHistory, usageHistoryLookbackDays,
			"rec[%d] expected %d daily points", i, usageHistoryLookbackDays)
	}
	// Two recs sharing the same tuple must result in only one CE call (batching).
	assert.Equal(t, 1, mock.coverageCalls, "identical tuples should share a single CE call")
}

// TestAttachDailyUsageHistory_SkipsEmptyRegion asserts that recs with a
// missing Region are silently skipped so AttachDailyUsageHistory never fires
// a CE call with an empty region filter value.
func TestAttachDailyUsageHistory_SkipsEmptyRegion(t *testing.T) {
	mock := &mockCoverageCE{}

	client := NewClientWithAPI(mock, "us-east-1")
	recs := []common.Recommendation{
		{
			Service:      common.ServiceEC2,
			Region:       "",
			ResourceType: "m5.large",
		},
	}

	client.AttachDailyUsageHistory(context.Background(), recs)

	assert.Nil(t, recs[0].UsageHistory, "rec with empty region must not have UsageHistory populated")
	assert.Equal(t, 0, mock.coverageCalls, "no CE call when region is empty")
}
