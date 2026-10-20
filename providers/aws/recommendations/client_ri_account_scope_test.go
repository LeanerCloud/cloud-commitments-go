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

type riScopeFixture struct {
	scopes []string
	inputs []map[string]any
}

func (f *riScopeFixture) Do(req *http.Request) (*http.Response, error) {
	var input map[string]any
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		return nil, err
	}
	f.inputs = append(f.inputs, input)
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

// An unrecognized term, payment option or lookback must fail before any
// Cost Explorer call: the zero-value enum is omitted from the request, CE
// applies its own defaults, and the parser would label the recs with the
// requested strings (for example "0yr").
func TestRIRecommendationsRejectUnrecognizedParams(t *testing.T) {
	t.Parallel()
	valid := common.RecommendationParams{Service: common.ServiceRDS, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d"}
	for _, tc := range []struct {
		name   string
		mutate func(*common.RecommendationParams)
		want   string
	}{
		{"zero term", func(p *common.RecommendationParams) { p.Term = "0yr" }, `unsupported term "0yr"`},
		{"unknown lookback", func(p *common.RecommendationParams) { p.LookbackPeriod = "14d" }, `unsupported lookback period "14d"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &riScopeFixture{}
			p := valid
			tc.mutate(&p)
			_, err := riScopeClient(f).GetRecommendations(context.Background(), &p)
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, f.scopes, "no Cost Explorer request may be sent")
		})
	}
}

// An unset ("") term, payment option or lookback is omitted from the request
// so Cost Explorer applies its own default, as before.
func TestRIRecommendationsUnsetParamsOmitted(t *testing.T) {
	t.Parallel()
	for field, mutate := range map[string]func(*common.RecommendationParams){
		"TermInYears":          func(p *common.RecommendationParams) { p.Term = "" },
		"PaymentOption":        func(p *common.RecommendationParams) { p.PaymentOption = "" },
		"LookbackPeriodInDays": func(p *common.RecommendationParams) { p.LookbackPeriod = "" },
	} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			f := &riScopeFixture{}
			p := common.RecommendationParams{Service: common.ServiceRDS, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d"}
			mutate(&p)
			_, err := riScopeClient(f).GetRecommendations(context.Background(), &p)
			require.NoError(t, err)
			require.Len(t, f.inputs, 1)
			require.NotContains(t, f.inputs[0], field)
			require.Len(t, f.inputs[0], 4, "the other fields stay set")
		})
	}
}
