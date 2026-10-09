package recommendations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/elasticache"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/memorydb"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/opensearch"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/rds"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/redshift"
)

type expiryTransport struct {
	t       *testing.T
	service string
	body    string
	calls   int
}

func (f *expiryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	host := f.service + ".us-east-1.amazonaws.com"
	if f.service == "opensearch" {
		host = "es.us-east-1.amazonaws.com"
	}
	if f.service == "memorydb" {
		host = "memory-db.us-east-1.amazonaws.com"
	}
	require.Equal(f.t, host, req.URL.Host)
	require.Equal(f.t, "https", req.URL.Scheme)
	contentType := "text/xml"
	switch f.service {
	case "memorydb":
		require.Equal(f.t, "POST", req.Method)
		require.Equal(f.t, "AmazonMemoryDB.DescribeReservedNodes", req.Header.Get("X-Amz-Target"))
		contentType = "application/x-amz-json-1.1"
	case "opensearch":
		require.Equal(f.t, "GET", req.Method)
		require.Equal(f.t, "/2021-01-01/opensearch/reservedInstances", req.URL.Path)
		contentType = "application/json"
	default:
		require.Equal(f.t, "POST", req.Method)
		require.NoError(f.t, req.ParseForm())
		actions := map[string]string{"rds": "DescribeReservedDBInstances", "elasticache": "DescribeReservedCacheNodes", "redshift": "DescribeReservedNodes"}
		require.Equal(f.t, actions[f.service], req.Form.Get("Action"))
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
		Body: io.NopCloser(strings.NewReader(f.body)), Request: req}, nil
}

type expiryRow struct {
	id       string
	start    *time.Time
	duration *int32
	count    int
}

func expiryClient(t *testing.T, service string, rows ...expiryRow) provider.ServiceClient {
	t.Helper()
	names := map[string][5]string{
		"rds":         {"ReservedDBInstance", "ReservedDBInstanceId", "DBInstanceClass", "DBInstanceCount", "db.r6g.large"},
		"elasticache": {"ReservedCacheNode", "ReservedCacheNodeId", "CacheNodeType", "CacheNodeCount", "cache.r6g.large"},
		"redshift":    {"ReservedNode", "ReservedNodeId", "NodeType", "NodeCount", "ra3.xlplus"},
		"memorydb":    {"ReservedNode", "ReservationId", "NodeType", "NodeCount", "db.r6g.large"},
		"opensearch":  {"ReservedInstance", "ReservedInstanceId", "InstanceType", "InstanceCount", "m5.large.search"},
	}
	n := names[service]
	records := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		record := map[string]any{n[1]: row.id, n[2]: n[4], n[3]: row.count, "State": "active"}
		if row.start != nil {
			record["StartTime"] = row.start.Format(time.RFC3339)
			if service == "memorydb" || service == "opensearch" {
				record["StartTime"] = row.start.Unix()
			}
		}
		if row.duration != nil {
			record["Duration"] = *row.duration
		}
		if service == "rds" {
			record["ProductDescription"], record["MultiAZ"] = "mysql", true
		}
		if service == "elasticache" {
			record["ProductDescription"] = "redis"
		}
		records = append(records, record)
	}
	var body string
	if service == "memorydb" || service == "opensearch" {
		data, err := json.Marshal(map[string]any{n[0] + "s": records})
		require.NoError(t, err)
		body = string(data)
	} else {
		var entries strings.Builder
		for _, record := range records {
			fmt.Fprintf(&entries, "<%s>", n[0])
			for key, value := range record {
				fmt.Fprintf(&entries, "<%s>%v</%s>", key, value, key)
			}
			fmt.Fprintf(&entries, "</%s>", n[0])
		}
		action := "Describe" + n[0] + "s"
		body = fmt.Sprintf("<%sResponse><%sResult><%ss>%s</%ss></%sResult></%sResponse>", action, action, n[0], entries.String(), n[0], action, action)
	}
	transport := &expiryTransport{t: t, service: service, body: body}
	t.Cleanup(func() { require.Positive(t, transport.calls) })
	cfg := aws.Config{Region: "us-east-1", HTTPClient: &http.Client{Transport: transport},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
		})}
	switch service {
	case "rds":
		return rds.NewClient(cfg)
	case "elasticache":
		return elasticache.NewClient(cfg)
	case "memorydb":
		return memorydb.NewClient(cfg)
	case "opensearch":
		return opensearch.NewClient(cfg)
	case "redshift":
		return redshift.NewClient(cfg)
	default:
		t.Fatalf("unsupported fixture service %s", service)
		return nil
	}
}

func TestReservationExpirySDKDecoders(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, service := range []string{"rds", "elasticache", "memorydb", "opensearch", "redshift"} {
		t.Run(service, func(t *testing.T) {
			client := expiryClient(t, service, expiryRow{"unknown", &start, nil, 2}, expiryRow{"known", &start, aws.Int32(31536000), 3})
			got, err := client.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			require.Len(t, got, 2)
			expected := common.Commitment{Provider: common.ProviderAWS, CommitmentID: "unknown", CommitmentType: common.CommitmentReservedInstance,
				Region: "us-east-1", Count: 2, State: common.CommitmentStateActive, StartDate: start}
			switch service {
			case "rds":
				expected.Service, expected.ResourceType, expected.Engine, expected.Deployment = common.ServiceRelationalDB, "db.r6g.large", "mysql", "multi-az"
			case "elasticache":
				expected.Service, expected.ResourceType, expected.Engine = common.ServiceCache, "cache.r6g.large", "redis"
			case "memorydb":
				expected.Service, expected.ResourceType, expected.Engine = common.ServiceMemoryDB, "db.r6g.large", "redis"
			case "opensearch":
				expected.Service, expected.ResourceType = common.ServiceSearch, "m5.large.search"
			case "redshift":
				expected.Service, expected.ResourceType = common.ServiceDataWarehouse, "ra3.xlplus"
			}
			assert.Equal(t, expected, got[0])
			expected.CommitmentID, expected.Count = "known", 3
			expected.EndDate = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			require.Equal(t, expected, got[1])
		})
	}
}

func TestReservationExpirySDKDates(t *testing.T) {
	start := time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC)
	zero := time.Time{}
	for _, tc := range []struct {
		name     string
		start    *time.Time
		duration int32
		want     time.Time
	}{
		{"zero-duration", &start, 0, zero},
		{"negative-duration", &start, -1, zero},
		{"missing-start", nil, 31536000, zero},
		{"zero-start", &zero, 31536000, zero},
		{"one-year-leap", &start, 31536000, time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"three-year-leap", &start, 94608000, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{"noncanonical-two-years", &start, 63072000, time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := expiryClient(t, "rds", expiryRow{"date", tc.start, &tc.duration, 2})
			got, err := client.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tc.want, got[0].EndDate)
			require.Equal(t, aws.ToTime(tc.start), got[0].StartDate)
		})
	}
}

func expiryRecommendation(resource string) common.Recommendation {
	return common.Recommendation{Provider: common.ProviderAWS, Service: common.ServiceCache, CommitmentType: common.CommitmentReservedInstance,
		Region: "us-east-1", ResourceType: resource, Count: 2, Term: "1yr", PaymentOption: "no-upfront",
		AverageInstancesUsedPerHour: 10, ExistingCoveragePct: 80, ExistingCoverageKnown: true, CommitmentCost: 100, EstimatedSavings: 20,
		Details: &common.CacheDetails{Engine: "redis"}}
}

func TestReservationExpirySDKSizing(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second).Add(-365 * 24 * time.Hour)
	for _, tc := range []struct {
		name     string
		duration *int32
		window   int
		adjusted int
	}{
		{"unknown", nil, 30, 0},
		{"expiring", aws.Int32(31536000), 30, 1},
		{"three-year", aws.Int32(94608000), 30, 0},
		{"disabled", aws.Int32(31536000), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := expiryClient(t, "elasticache", expiryRow{"owned", &start, tc.duration, 2})
			commits, err := client.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			recs := []common.Recommendation{expiryRecommendation("cache.r6g.large"), expiryRecommendation("cache.r7g.large")}
			require.Equal(t, tc.adjusted, recommendations.AdjustExistingCoverageForExpiringCommitments(recs, commits, tc.window))
			require.Equal(t, float64(80), recs[1].ExistingCoveragePct)
			got := recfilter.ApplyTargetCoverage(recs, 80, nil, nil)
			if tc.adjusted == 0 {
				require.Empty(t, got)
			} else {
				require.Len(t, got, 1)
				require.Equal(t, "cache.r6g.large", got[0].ResourceType)
				require.Equal(t, 2, got[0].Count)
				require.Equal(t, float64(60), got[0].ExistingCoveragePct)
			}
		})
	}
}

func TestReservationExpirySDKDedupe(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	client := expiryClient(t, "elasticache", expiryRow{"recent", &start, nil, 2})
	recs := []common.Recommendation{expiryRecommendation("cache.r6g.large"), expiryRecommendation("cache.r7g.large")}
	got, dropped, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, client)
	require.NoError(t, err)
	require.Equal(t, recs[1:], got)
	require.Equal(t, recs[:1], dropped)
}

func TestReservationExpirySDKRDSAdjustment(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second).Add(-365 * 24 * time.Hour)
	client := expiryClient(t, "rds", expiryRow{"unknown", &start, nil, 2})
	commits, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	rec := expiryRecommendation("db.r6g.large")
	rec.Service = common.ServiceRelationalDB
	rec.Details = &common.DatabaseDetails{Engine: "mysql", AZConfig: "multi-az"}
	recs := []common.Recommendation{rec}
	require.Zero(t, recommendations.AdjustExistingCoverageForExpiringCommitments(recs, commits, 30))
	require.Equal(t, rec, recs[0])
}
