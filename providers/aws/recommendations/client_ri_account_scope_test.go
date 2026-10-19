package recommendations_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type riScopeFixture struct{ scopes []string }

func (f *riScopeFixture) Do(req *http.Request) (*http.Response, error) {
	var input map[string]any
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		return nil, err
	}
	scope, _ := input["AccountScope"].(string)
	f.scopes = append(f.scopes, scope)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
		Body: io.NopCloser(strings.NewReader(`{"Recommendations":[]}`)), Request: req}, nil
}

func riScopeClient(f *riScopeFixture) *recommendations.Client {
	return recommendations.NewClient(&aws.Config{Region: "us-east-1", HTTPClient: f, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
		})})
}

func TestRIAccountScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"", "LINKED"}, {"linked", "LINKED"}, {"payer", "PAYER"}, {"PAYER", "PAYER"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			f := &riScopeFixture{}
			_, err := riScopeClient(f).GetRecommendations(context.Background(), &common.RecommendationParams{
				Service: common.ServiceRDS, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d", RIAccountScope: tc.in})
			require.NoError(t, err)
			require.Equal(t, []string{tc.want}, f.scopes)
		})
	}
}

func TestRIAccountScopeRejectsUnknown(t *testing.T) {
	t.Parallel()
	f := &riScopeFixture{}
	_, err := riScopeClient(f).GetRecommendations(context.Background(), &common.RecommendationParams{
		Service: common.ServiceRDS, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d", RIAccountScope: "org"})
	require.ErrorContains(t, err, "invalid RI account scope")
	require.Empty(t, f.scopes)
}
