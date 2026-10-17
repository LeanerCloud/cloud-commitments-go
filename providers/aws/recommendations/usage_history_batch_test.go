package recommendations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// coverageFixture is one in-memory CE coverage table served in both request
// shapes: per-type DAILY queries (filtered by INSTANCE_TYPE, read from Total)
// and per-day grouped queries (GroupBy INSTANCE_TYPE, read from Groups). A
// nil *string value models CE returning the row without a percentage.
type coverageFixture struct {
	t        *testing.T
	pageSize int                                      // grouped groups per page; 0 = single page
	data     map[string]map[string]map[string]*string // region -> type -> day -> pct
	failDay  string                                   // grouped request for this day returns an error
	nextTok  func(sent string) string                 // when set, grouped pages carry no groups and this NextPageToken

	mu      sync.Mutex
	calls   map[string]int // region -> requests
	grouped int
}

func (f *coverageFixture) serve(w http.ResponseWriter, r *http.Request) {
	var input costexplorer.GetReservationCoverageInput
	if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&input)) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	region := filterValue(input.Filter, types.DimensionRegion)
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[region]++
	if len(input.GroupBy) > 0 {
		f.grouped++
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	var out map[string]any
	if len(input.GroupBy) > 0 {
		out = f.groupedResponse(w, &input, region)
	} else {
		out = f.perTypeResponse(&input, region)
	}
	if out != nil {
		assert.NoError(f.t, json.NewEncoder(w).Encode(out))
	}
}

func (f *coverageFixture) perTypeResponse(input *costexplorer.GetReservationCoverageInput, region string) map[string]any {
	assert.Equal(f.t, types.GranularityDaily, input.Granularity)
	rt := filterValue(input.Filter, types.DimensionInstanceType)
	start, err := time.Parse("2006-01-02", aws.ToString(input.TimePeriod.Start))
	require.NoError(f.t, err)
	periods := []types.CoverageByTime{}
	for i := 0; i < usageHistoryLookbackDays; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		pct, ok := f.data[region][rt][day]
		if !ok {
			continue
		}
		p := types.CoverageByTime{TimePeriod: &types.DateInterval{Start: aws.String(day)}}
		if pct != nil {
			p.Total = &types.Coverage{CoverageHours: &types.CoverageHours{CoverageHoursPercentage: pct}}
		}
		periods = append(periods, p)
	}
	return map[string]any{"CoveragesByTime": periods}
}

func (f *coverageFixture) groupedResponse(w http.ResponseWriter, input *costexplorer.GetReservationCoverageInput, region string) map[string]any {
	day := aws.ToString(input.TimePeriod.Start)
	start, err := time.Parse("2006-01-02", day)
	require.NoError(f.t, err)
	assert.Empty(f.t, input.Granularity, "CE rejects Granularity with GroupBy")
	assert.Equal(f.t, start.AddDate(0, 0, 1).Format("2006-01-02"), aws.ToString(input.TimePeriod.End))
	assert.Equal(f.t, []types.GroupDefinition{{Type: types.GroupDefinitionTypeDimension, Key: aws.String("INSTANCE_TYPE")}}, input.GroupBy)
	assert.Equal(f.t, serviceRegionFilter(ec2CoverageService, region), input.Filter)
	assert.Equal(f.t, []string{"Hour"}, input.Metrics)
	if day == f.failDay {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"DataUnavailableException","message":"no coverage available"}`))
		return nil
	}
	if f.nextTok != nil {
		period := types.CoverageByTime{TimePeriod: &types.DateInterval{Start: aws.String(day)}}
		return map[string]any{"CoveragesByTime": []types.CoverageByTime{period}, "NextPageToken": f.nextTok(aws.ToString(input.NextPageToken))}
	}
	groups := f.dayGroups(region, day)
	offset := 0
	if tok := aws.ToString(input.NextPageToken); tok != "" {
		_, err := fmt.Sscanf(tok, "page-%d", &offset)
		require.NoError(f.t, err)
	}
	end := len(groups)
	var next *string
	if f.pageSize > 0 && offset+f.pageSize < end {
		end = offset + f.pageSize
		next = aws.String(fmt.Sprintf("page-%d", end))
	}
	period := types.CoverageByTime{TimePeriod: &types.DateInterval{Start: aws.String(day)}, Groups: groups[offset:end]}
	return map[string]any{"CoveragesByTime": []types.CoverageByTime{period}, "NextPageToken": next}
}

// dayGroups returns the fixture's groups for one day in a stable order, plus
// an unrequested instance type that the client must ignore.
func (f *coverageFixture) dayGroups(region, day string) []types.ReservationCoverageGroup {
	names := make([]string, 0, len(f.data[region]))
	for rt := range f.data[region] {
		names = append(names, rt)
	}
	sort.Strings(names)
	groups := []types.ReservationCoverageGroup{{
		Attributes: map[string]string{"instanceType": "z9.unrequested"},
		Coverage:   &types.Coverage{CoverageHours: &types.CoverageHours{CoverageHoursPercentage: aws.String("1")}},
	}}
	for _, rt := range names {
		pct, ok := f.data[region][rt][day]
		if !ok {
			continue
		}
		g := types.ReservationCoverageGroup{Attributes: map[string]string{"instanceType": rt, "region": region}}
		if pct != nil {
			g.Coverage = &types.Coverage{CoverageHours: &types.CoverageHours{CoverageHoursPercentage: pct}}
		}
		groups = append(groups, g)
	}
	return groups
}

// counts returns the requests seen for region and the grouped request total.
func (f *coverageFixture) counts(region string) (perRegion, grouped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[region], f.grouped
}

func filterValue(expr *types.Expression, key types.Dimension) string {
	if expr == nil {
		return ""
	}
	for _, e := range expr.And {
		if e.Dimensions != nil && e.Dimensions.Key == key && len(e.Dimensions.Values) == 1 {
			return e.Dimensions.Values[0]
		}
	}
	return ""
}

// usageWindowDays returns the lookback window's day strings, matching the
// client's own window computation.
func usageWindowDays() []string {
	start := time.Now().UTC().AddDate(0, 0, -usageHistoryLookbackDays)
	days := make([]string, usageHistoryLookbackDays)
	for i := range days {
		days[i] = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	return days
}

// manyTypeFixture builds n instance types for one region. Type i has data on
// days where (i+d)%3 != 0, a row without a percentage on day 1 for type 0, the
// second-to-last type has only rows without a percentage, and the last type
// has no data at all.
func manyTypeFixture(n int) (map[string]map[string]*string, []string) {
	days := usageWindowDays()
	data := make(map[string]map[string]*string, n)
	names := make([]string, n)
	for i := 0; i < n; i++ {
		rt := fmt.Sprintf("m%d.large", i+1)
		names[i] = rt
		data[rt] = map[string]*string{}
		if i == n-1 {
			continue // missing from every response
		}
		for d, day := range days {
			if (i+d)%3 != 0 {
				data[rt][day] = aws.String(fmt.Sprintf("%d.5", 10*i+d))
			}
		}
	}
	data[names[0]][days[1]] = nil
	for _, day := range days {
		data[names[n-2]][day] = nil
	}
	return data, names
}

func newFixtureClient(t *testing.T, f *coverageFixture) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	sdk := costexplorer.New(costexplorer.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(server.URL),
		Credentials: aws.AnonymousCredentials{}, HTTPClient: server.Client(),
	})
	return NewClientWithAPI(sdk, "us-east-1")
}

func recsFor(region string, names []string) []common.Recommendation {
	recs := make([]common.Recommendation, 0, len(names)+1)
	for _, rt := range names {
		recs = append(recs, common.Recommendation{Service: common.ServiceEC2, Region: region, ResourceType: rt})
	}
	// A second rec sharing the first SKU must receive the same series.
	return append(recs, common.Recommendation{Service: common.ServiceEC2, Region: region, ResourceType: names[0]})
}

// legacySeries computes each type's series through the per-type path
// (GetDailyUsagePcts), i.e. what callers received before batching.
func legacySeries(t *testing.T, data map[string]map[string]map[string]*string, region string, names []string) map[string][]float64 {
	t.Helper()
	legacy := newFixtureClient(t, &coverageFixture{t: t, data: data})
	out := make(map[string][]float64, len(names))
	for _, rt := range names {
		pcts, err := legacy.GetDailyUsagePcts(context.Background(), ec2CoverageService, rt, region)
		require.NoError(t, err)
		out[rt] = pcts
	}
	return out
}

func TestAttachDailyUsageHistory_BatchesManyTypesPerRegion(t *testing.T) {
	t.Parallel()
	const region = "us-east-1"
	regionData, names := manyTypeFixture(12)
	data := map[string]map[string]map[string]*string{region: regionData}
	want := legacySeries(t, data, region, names)

	for _, tc := range []struct {
		name      string
		pageSize  int
		wantCalls int
	}{
		// Before batching: one request per instance type (12).
		{name: "single page", wantCalls: usageHistoryLookbackDays},
		// Each day has 1 unrequested + up to 11 typed groups; pages of 4.
		{name: "paginated", pageSize: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &coverageFixture{t: t, data: data, pageSize: tc.pageSize}
			client := newFixtureClient(t, f)
			recs := recsFor(region, names)
			client.AttachDailyUsageHistory(context.Background(), recs)

			wantCalls := tc.wantCalls
			if tc.pageSize > 0 {
				for _, day := range usageWindowDays() {
					n := len(f.dayGroups(region, day))
					wantCalls += (n + tc.pageSize - 1) / tc.pageSize
				}
			}
			calls, grouped := f.counts(region)
			assert.Equal(t, wantCalls, calls, "CE requests for %d instance types", len(names))
			assert.Equal(t, wantCalls, grouped, "every request should be a grouped request")
			for _, r := range recs {
				assert.Equal(t, want[r.ResourceType], r.UsageHistory, "series for %s must match the per-type path", r.ResourceType)
			}
			assert.Nil(t, recs[len(names)-1].UsageHistory, "type missing from every response stays nil")
			assert.Nil(t, recs[len(names)-2].UsageHistory, "type with only percentage-less rows stays nil")
			require.Len(t, recs[0].UsageHistory, usageHistoryLookbackDays)
			assert.Equal(t, 0.0, recs[0].UsageHistory[1], "row without a percentage keeps the zero slot")
		})
	}
}

func TestAttachDailyUsageHistory_SmallBucketStaysPerType(t *testing.T) {
	t.Parallel()
	const region = "eu-west-1"
	regionData, names := manyTypeFixture(3)
	data := map[string]map[string]map[string]*string{region: regionData}
	want := legacySeries(t, data, region, names)
	f := &coverageFixture{t: t, data: data}
	recs := recsFor(region, names)
	newFixtureClient(t, f).AttachDailyUsageHistory(context.Background(), recs)

	calls, grouped := f.counts(region)
	assert.Equal(t, len(names), calls, "a small bucket keeps one request per type")
	assert.Zero(t, grouped)
	for _, r := range recs {
		assert.Equal(t, want[r.ResourceType], r.UsageHistory)
	}
}

func TestAttachDailyUsageHistory_GroupedErrorLeavesBucketNil(t *testing.T) {
	t.Parallel()
	big, bigNames := manyTypeFixture(9)
	small, smallNames := manyTypeFixture(4)
	data := map[string]map[string]map[string]*string{"us-east-1": big, "us-west-2": small}
	f := &coverageFixture{t: t, data: data, failDay: usageWindowDays()[3]}
	recs := append(recsFor("us-east-1", bigNames), recsFor("us-west-2", smallNames)...)
	newFixtureClient(t, f).AttachDailyUsageHistory(context.Background(), recs)

	for _, r := range recs {
		if r.Region == "us-east-1" {
			assert.Nil(t, r.UsageHistory, "%s: a CE error must not be reported as coverage", r.ResourceType)
		}
	}
	firstSmall := len(bigNames) + 1 // recsFor appends one duplicate rec per region
	assert.Equal(t, "us-west-2", recs[firstSmall].Region)
	assert.NotNil(t, recs[firstSmall].UsageHistory, "other region buckets are unaffected")
	calls, _ := f.counts("us-east-1")
	assert.Equal(t, 4, calls, "fetch stops at the failing day")

	_, err := newFixtureClient(t, f).getDailyUsagePctsByType(context.Background(), ec2CoverageService, "us-east-1", bigNames)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "DataUnavailableException"), err.Error())
}

// fetchOneGroupedDay runs one grouped day against tokens served by nextTok
// and returns the grouped request count and the error.
func fetchOneGroupedDay(t *testing.T, nextTok func(string) string) (int, error) {
	t.Helper()
	data, _ := manyTypeFixture(9)
	f := &coverageFixture{t: t, data: map[string]map[string]map[string]*string{"us-east-1": data}, nextTok: nextTok}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	day, err := time.Parse("2006-01-02", usageWindowDays()[0])
	require.NoError(t, err)
	err = newFixtureClient(t, f).fetchGroupedDayCoverage(ctx, ec2CoverageService, "us-east-1", day, &groupedDayAccumulator{})
	_, grouped := f.counts("us-east-1")
	return grouped, err
}

func TestFetchGroupedDayCoverage_RepeatedPageTokenFails(t *testing.T) {
	t.Parallel()
	grouped, err := fetchOneGroupedDay(t, func(string) string { return "same" })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repeated page token")
	assert.Equal(t, 2, grouped, "stops at the first repeated token")
}

func TestFetchGroupedDayCoverage_EndlessFreshTokensHitCap(t *testing.T) {
	t.Parallel()
	grouped, err := fetchOneGroupedDay(t, func(sent string) string {
		n := 0
		if sent != "" {
			_, err := fmt.Sscanf(sent, "page-%d", &n)
			require.NoError(t, err)
		}
		return fmt.Sprintf("page-%d", n+1)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("pagination cap reached after %d pages", maxCoveragePages))
	assert.Equal(t, maxCoveragePages, grouped, "requests stop at the cap")
}

func TestFetchGroupedDayCoverage_TokenCycleHitsCap(t *testing.T) {
	t.Parallel()
	grouped, err := fetchOneGroupedDay(t, func(sent string) string {
		if sent == "A" {
			return "B"
		}
		return "A"
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Equal(t, maxCoveragePages, grouped, "an A, B, A, B cycle stops at the cap")
}

func TestAttachDailyUsageHistory_EndlessGroupedTokensLeaveBucketNil(t *testing.T) {
	t.Parallel()
	data, names := manyTypeFixture(9)
	f := &coverageFixture{t: t, data: map[string]map[string]map[string]*string{"us-east-1": data}, nextTok: func(sent string) string { return sent + "x" }}
	recs := recsFor("us-east-1", names)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	newFixtureClient(t, f).AttachDailyUsageHistory(ctx, recs)
	for _, r := range recs {
		assert.Nil(t, r.UsageHistory, "%s: a capped fetch must not be reported as coverage", r.ResourceType)
	}
	_, grouped := f.counts("us-east-1")
	assert.Equal(t, maxCoveragePages, grouped, "fetch stops at the cap on the first day")
}

// TestGetDailyUsagePctsByType_MalformedGroupPercentageFailsLoud: a malformed
// grouped CoverageHoursPercentage used to become a fake 0% day.
func TestGetDailyUsagePctsByType_MalformedGroupPercentageFailsLoud(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"oops", "", "NaN", "-1"} {
		t.Run("bad_"+bad, func(t *testing.T) {
			t.Parallel()
			data, names := manyTypeFixture(9)
			data[names[0]][usageWindowDays()[2]] = aws.String(bad)
			f := &coverageFixture{t: t, data: map[string]map[string]map[string]*string{"us-east-1": data}}
			got, err := newFixtureClient(t, f).getDailyUsagePctsByType(context.Background(), ec2CoverageService, "us-east-1", names)
			require.Error(t, err)
			assert.Nil(t, got)
			for _, want := range []string{"CoverageHoursPercentage", names[0], "page 1"} {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestGroupedDayAccumulator_NilPercentageIsSkipped(t *testing.T) {
	t.Parallel()
	acc := &groupedDayAccumulator{
		dayPct:   map[string]map[string]float64{"m1.large": {}},
		byLower:  map[string][]string{"m1.large": {"m1.large"}},
		withData: map[string]bool{},
	}
	periods := []types.CoverageByTime{{
		TimePeriod: &types.DateInterval{Start: aws.String("2026-01-01")},
		Groups: []types.ReservationCoverageGroup{
			{Attributes: map[string]string{"instanceType": "m1.large"}},
		},
	}}
	require.NoError(t, acc.addPeriods(periods))
	assert.Empty(t, acc.dayPct["m1.large"])
	assert.False(t, acc.withData["m1.large"])
}

func TestGroupedDayAccumulator_MalformedUnrequestedTypeIgnored(t *testing.T) {
	t.Parallel()
	newAcc := func() *groupedDayAccumulator {
		return &groupedDayAccumulator{
			dayPct:   map[string]map[string]float64{"m1.large": {}},
			byLower:  map[string][]string{"m1.large": {"m1.large"}},
			withData: map[string]bool{},
		}
	}
	group := func(typ, pct string) types.ReservationCoverageGroup {
		return types.ReservationCoverageGroup{
			Attributes: map[string]string{"instanceType": typ},
			Coverage:   &types.Coverage{CoverageHours: &types.CoverageHours{CoverageHoursPercentage: aws.String(pct)}},
		}
	}
	period := func(groups ...types.ReservationCoverageGroup) []types.CoverageByTime {
		return []types.CoverageByTime{{TimePeriod: &types.DateInterval{Start: aws.String("2026-01-01")}, Groups: groups}}
	}

	acc := newAcc()
	require.NoError(t, acc.addPeriods(period(group("z9.unrequested", "oops"), group("m1.large", "42"))))
	assert.InDelta(t, 42.0, acc.dayPct["m1.large"]["2026-01-01"], 0.001)

	require.Error(t, newAcc().addPeriods(period(group("m1.large", "oops"))))
}
