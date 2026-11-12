package recommendations

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const familyTestAccount = "123456789012"

// groupedDay builds one grouped ResultByTime row. groups maps an INSTANCE_TYPE
// key to that day's UnblendedCost in USD.
func groupedDay(day string, groups map[string]float64) types.ResultByTime {
	d, _ := time.Parse(ceDateLayout, day)
	r := types.ResultByTime{TimePeriod: &types.DateInterval{
		Start: aws.String(day),
		End:   aws.String(d.AddDate(0, 0, 1).Format(ceDateLayout)),
	}}
	for k, usd := range groups {
		r.Groups = append(r.Groups, types.Group{
			Keys:    []string{k},
			Metrics: map[string]types.MetricValue{onDemandMetric: {Amount: aws.String(fmt.Sprint(usd))}},
		})
	}
	return r
}

// windowDays returns the lookbackDays calendar days ending yesterday, oldest first.
func windowDays(lookbackDays int) []string {
	end := time.Now().UTC().Truncate(24 * time.Hour)
	out := make([]string, lookbackDays)
	for i := range out {
		out[i] = end.AddDate(0, 0, -lookbackDays+i).Format(ceDateLayout)
	}
	return out
}

func TestGetOnDemandFamilySeries_GroupsByFamilyAndZeroFillsAbsentDays(t *testing.T) {
	days := windowDays(3)
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{{ResultsByTime: []types.ResultByTime{
		groupedDay(days[0], map[string]float64{"m5.large": 24, "m5.xlarge": 216, "c5.large": 24}),
		groupedDay(days[1], map[string]float64{"m5.large": 24}),
		groupedDay(days[2], map[string]float64{"c5.large": 48}),
	}}}}
	got, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 3)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.InDelta(t, 10.0, got["m5"][0].USDPerHour, 1e-9, "m5 day0 = 240/24")
	assert.InDelta(t, 1.0, got["m5"][1].USDPerHour, 1e-9)
	assert.InDelta(t, 0.0, got["m5"][2].USDPerHour, 1e-9, "absent day is zero, not dropped")
	assert.InDelta(t, 1.0, got["c5"][0].USDPerHour, 1e-9)
	assert.InDelta(t, 0.0, got["c5"][1].USDPerHour, 1e-9)
	assert.InDelta(t, 2.0, got["c5"][2].USDPerHour, 1e-9)
	assert.Len(t, got["m5"], 3)
}

func TestGetOnDemandFamilySeries_SkipsNoInstanceType(t *testing.T) {
	days := windowDays(1)
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{{ResultsByTime: []types.ResultByTime{
		groupedDay(days[0], map[string]float64{"NoInstanceType": 240, "m5.large": 24}),
	}}}}
	got, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 1)
	require.NoError(t, err)
	require.Len(t, got, 1, "NoInstanceType belongs to no family and must not appear")
	assert.InDelta(t, 1.0, got["m5"][0].USDPerHour, 1e-9)
}

func TestGetOnDemandFamilySeries_SumsDayAcrossPages(t *testing.T) {
	days := windowDays(1)
	page1 := &costexplorer.GetCostAndUsageOutput{
		ResultsByTime: []types.ResultByTime{groupedDay(days[0], map[string]float64{"m5.large": 24})},
		NextPageToken: aws.String("t1"),
	}
	page2 := &costexplorer.GetCostAndUsageOutput{
		ResultsByTime: []types.ResultByTime{groupedDay(days[0], map[string]float64{"m5.xlarge": 24})},
	}
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{page1, page2}, tokens: []string{"t1"}}
	got, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 1)
	require.NoError(t, err)
	assert.InDelta(t, 2.0, got["m5"][0].USDPerHour, 1e-9, "a day split across pages and types is summed, not overwritten")
}

func TestGetOnDemandFamilySeries_RejectsKeyWithoutFamily(t *testing.T) {
	days := windowDays(1)
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{{ResultsByTime: []types.ResultByTime{
		groupedDay(days[0], map[string]float64{"large": 24}),
	}}}}
	_, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no family prefix")
}

func TestGetOnDemandFamilySeries_MissingMetricFails(t *testing.T) {
	days := windowDays(1)
	row := groupedDay(days[0], map[string]float64{"m5.large": 24})
	row.Groups[0].Metrics = map[string]types.MetricValue{}
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{{ResultsByTime: []types.ResultByTime{row}}}}
	_, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 1)
	require.Error(t, err)
}

func TestGetOnDemandFamilySeries_EmptyResultReturnsNoFamilies(t *testing.T) {
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{{}}}
	got, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 3)
	require.NoError(t, err)
	assert.Empty(t, got, "no family data is the caller's fallback case, not an error")
}

func TestGetOnDemandFamilySeries_InputShape(t *testing.T) {
	days := windowDays(1)
	m := &mockOnDemandCE{pages: []*costexplorer.GetCostAndUsageOutput{{ResultsByTime: []types.ResultByTime{
		groupedDay(days[0], map[string]float64{"m5.large": 24}),
	}}}}
	_, err := newOnDemandClient(m).GetOnDemandFamilySeriesForAccount(context.Background(), familyTestAccount, "us-east-1", 1)
	require.NoError(t, err)
	require.Len(t, m.gotInputs, 1)
	in := m.gotInputs[0]
	assert.Equal(t, types.GranularityDaily, in.Granularity)
	require.Len(t, in.GroupBy, 1)
	assert.Equal(t, "INSTANCE_TYPE", aws.ToString(in.GroupBy[0].Key))
	require.Len(t, in.Filter.And, 4, "SERVICE, PURCHASE_TYPE, REGION and LINKED_ACCOUNT")
	last := in.Filter.And[3].Dimensions
	assert.Equal(t, types.DimensionLinkedAccount, last.Key)
	assert.Equal(t, []string{familyTestAccount}, last.Values)
}

func TestGetOnDemandFamilySeries_RejectsEmptyAccount(t *testing.T) {
	_, err := newOnDemandClient(&mockOnDemandCE{}).GetOnDemandFamilySeriesForAccount(context.Background(), "", "us-east-1", 1)
	require.Error(t, err)
}
