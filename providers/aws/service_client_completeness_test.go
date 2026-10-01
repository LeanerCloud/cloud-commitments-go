package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type recommendationHTTPFixture func(*http.Request) (*http.Response, error)

func (f recommendationHTTPFixture) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestRecommendationsClientAdapter_IncompleteSDKResponse(t *testing.T) {
	calls := 0
	client := NewRecommendationsClient(aws.Config{
		Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
		}),
		HTTPClient: recommendationHTTPFixture(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("X-Amz-Target") != "AWSInsightsIndexService.GetReservationPurchaseRecommendation" {
				return nil, fmt.Errorf("unexpected SDK request: %s", req.Header.Get("X-Amz-Target"))
			}
			calls++
			body := `{"Recommendations":[{"RecommendationDetails":[
				{"RecommendedNumberOfInstancesToPurchase":"2","EstimatedMonthlyOnDemandCost":"30","AccountId":"included","InstanceDetails":{"RDSInstanceDetails":{"InstanceType":"db.t3.medium","Region":"us-east-1"}}},
				{"RecommendedNumberOfInstancesToPurchase":"1","EstimatedMonthlyOnDemandCost":"30","AccountId":"excluded","InstanceDetails":{"RDSInstanceDetails":{"InstanceType":"db.t3.medium","Region":"us-east-1"}}},
				{"RecommendedNumberOfInstancesToPurchase":"1","EstimatedMonthlyOnDemandCost":"30","AccountId":"included","InstanceDetails":{"RDSInstanceDetails":{"InstanceType":"db.t3.medium","Region":"eu-west-1"}}},
				{"RecommendedNumberOfInstancesToPurchase":"not-a-number"}]}]}`
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		}),
	})
	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{
		Service: common.ServiceRDS, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d",
		Region: "us-east-1", AccountFilter: []string{"included"},
	})
	require.Error(t, err, "malformed detail must not produce a successful short menu")
	var incomplete *recommendations.IncompleteRecommendationsError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, 1, incomplete.FailedDetails)
	require.Zero(t, incomplete.FailedScopes)
	require.Len(t, incomplete.Causes, 1)
	require.Contains(t, incomplete.Error(), "service rds term 1yr payment no-upfront block 0 detail 3")
	require.Len(t, recs, 1, "incomplete survivors must still receive account and region filters")
	require.Equal(t, 2, recs[0].Count)
	require.Equal(t, 1, calls)
}
