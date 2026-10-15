package ladder_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"
)

// Constructed CE responses exercise the public factory without cloud access.
func TestScopedBaselineParentCompatible(t *testing.T) {
	for _, mode := range []string{"account", "reverse", "zero", "lag2", "lag3", "duplicate", "page-duplicate", "hole", "leading", "outside", "future", "bad-start", "nil-start", "nil-period", "nil-end", "bad-end", "short-period", "long-period", "backward-period", "empty", "all-zero", "missing-total", "missing-metric", "missing-amount", "bad-amount", "NaN", "Inf", "negative", "stale", "few", "provider-error"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "AWSInsightsIndexService.GetCostAndUsage", r.Header.Get("X-Amz-Target"))
				var in costexplorer.GetCostAndUsageInput
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&in)) {
					return
				}
				assert.Equal(t, types.GranularityDaily, in.Granularity)
				assert.Equal(t, []string{"UnblendedCost"}, in.Metrics)
				start, err := time.Parse(time.DateOnly, aws.ToString(in.TimePeriod.Start))
				assert.NoError(t, err)
				assert.Equal(t, start.AddDate(0, 0, 30).Format(time.DateOnly), aws.ToString(in.TimePeriod.End))
				dimensions := map[types.Dimension][]string{}
				if in.Filter != nil {
					for _, clause := range in.Filter.And {
						if clause.Dimensions != nil {
							dimensions[clause.Dimensions.Key] = clause.Dimensions.Values
						}
					}
				}
				matched := slices.Equal(dimensions[types.DimensionLinkedAccount], []string{"fixture-account"}) &&
					slices.Equal(dimensions[types.DimensionRegion], []string{"eu-west-1"}) &&
					slices.Equal(dimensions[types.DimensionService], []string{"Amazon Elastic Compute Cloud - Compute"}) &&
					slices.Equal(dimensions[types.DimensionPurchaseType], []string{"On Demand Instances"})
				rows := baselineRows(start, mode)
				token := ""
				if mode == "account" || mode == "reverse" || mode == "page-duplicate" {
					switch aws.ToString(in.NextPageToken) {
					case "":
						rows, token = rows[:28], "second"
					case "second":
						rows = rows[28:]
						if mode == "page-duplicate" {
							rows = append(rows, baselineRows(start, "account")[0])
						}
					default:
						t.Error("unexpected page token")
					}
				}
				if mode == "reverse" {
					slices.Reverse(rows)
				}
				if mode == "account" && !matched {
					for i := range rows {
						rows[i].Total["UnblendedCost"] = types.MetricValue{Amount: aws.String("2400")}
					}
				}
				if mode == "provider-error" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ResultsByTime": rows, "NextPageToken": token}))
			}))
			defer srv.Close()
			endpoint, err := url.Parse(srv.URL)
			require.NoError(t, err)
			a, err := awsladder.NewFromConfig(aws.Config{Region: "eu-west-1", RetryMaxAttempts: 1,
				Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""),
				HTTPClient:  &http.Client{Transport: ceLoopback{endpoint: endpoint, transport: srv.Client().Transport}},
			}, awsladder.Config{Region: "eu-west-1", AccountID: "fixture-account"})
			require.NoError(t, err)
			scope := ladder.Scope{Provider: common.ProviderAWS, AccountID: "wrong"}
			_, err = a.GetUsageBaseline(context.Background(), scope, 30, 5)
			require.Error(t, err)
			assert.Zero(t, calls.Load())
			scope.AccountID = "fixture-account"
			_, err = a.GetUsageBaseline(context.Background(), scope, 0, 5)
			require.Error(t, err)
			_, err = a.GetUsageBaseline(context.Background(), scope, 30, 0)
			require.Error(t, err)
			assert.Zero(t, calls.Load())
			bl, err := a.GetUsageBaseline(context.Background(), scope, 30, 5)
			want := map[string]float64{"account": 2, "reverse": 2, "zero": 2, "lag2": 10, "lag3": 10}
			if low, ok := want[mode]; ok {
				require.NoError(t, err)
				require.NotNil(t, bl.LowWaterUSDPerHour)
				assert.InDelta(t, low, *bl.LowWaterUSDPerHour, 1e-9)
				assert.Nil(t, bl.StableUSDPerHour)
				assert.Equal(t, 30, bl.LookbackDays)
				assert.Equal(t, 5.0, bl.Percentile)
				assert.Equal(t, 10.0, bl.Series[1])
				if mode == "zero" {
					assert.Zero(t, bl.Series[0])
				}
			} else {
				require.Error(t, err)
			}
			if mode == "account" || mode == "reverse" {
				assert.Equal(t, int32(2), calls.Load())
			}
		})
	}
}

func baselineRows(start time.Time, mode string) []types.ResultByTime {
	rows := make([]types.ResultByTime, 30)
	for i := range rows {
		day, amount := start.AddDate(0, 0, i), "240"
		if i >= 28 {
			amount = "48"
		}
		rows[i] = types.ResultByTime{TimePeriod: &types.DateInterval{Start: aws.String(day.Format(time.DateOnly)), End: aws.String(day.AddDate(0, 0, 1).Format(time.DateOnly))}, Total: map[string]types.MetricValue{"UnblendedCost": {Amount: aws.String(amount), Unit: aws.String("USD")}}}
	}
	switch mode {
	case "duplicate":
		rows = append(rows, rows[0])
	case "hole":
		rows = append(rows[:10], rows[11:]...)
	case "leading":
		rows = rows[1:]
	case "outside":
		rows[0].TimePeriod.Start = aws.String(start.AddDate(0, 0, -1).Format(time.DateOnly))
		rows[0].TimePeriod.End = aws.String(start.Format(time.DateOnly))
	case "future":
		rows[0].TimePeriod = &types.DateInterval{Start: aws.String(start.AddDate(0, 0, 30).Format(time.DateOnly)), End: aws.String(start.AddDate(0, 0, 31).Format(time.DateOnly))}
	case "bad-start":
		rows[0].TimePeriod.Start = aws.String("bad")
	case "nil-start":
		rows[0].TimePeriod.Start = nil
	case "nil-period":
		rows[0].TimePeriod = nil
	case "nil-end":
		rows[0].TimePeriod.End = nil
	case "bad-end":
		rows[0].TimePeriod.End = aws.String("bad")
	case "short-period":
		rows[0].TimePeriod.End = rows[0].TimePeriod.Start
	case "long-period":
		rows[0].TimePeriod.End = aws.String(start.AddDate(0, 0, 2).Format(time.DateOnly))
	case "backward-period":
		rows[0].TimePeriod.End = aws.String(start.AddDate(0, 0, -1).Format(time.DateOnly))
	case "empty":
		rows = nil
	case "all-zero":
		for i := range rows {
			rows[i].Total["UnblendedCost"] = types.MetricValue{Amount: aws.String("0")}
		}
	case "missing-total":
		rows[0].Total = nil
	case "missing-metric":
		rows[0].Total = map[string]types.MetricValue{}
	case "missing-amount":
		rows[0].Total["UnblendedCost"] = types.MetricValue{}
	case "bad-amount", "NaN", "Inf", "negative", "zero":
		amount := map[string]string{"bad-amount": "bad", "NaN": "NaN", "Inf": "Inf", "negative": "-1", "zero": "0"}[mode]
		rows[0].Total["UnblendedCost"] = types.MetricValue{Amount: aws.String(amount)}
	case "lag2", "lag3", "stale", "few":
		rows = rows[:map[string]int{"lag2": 29, "lag3": 28, "stale": 27, "few": 6}[mode]]
	}
	return rows
}
