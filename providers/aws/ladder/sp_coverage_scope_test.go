package ladder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type spScopeFixture struct {
	t        *testing.T
	pages    map[string][]string
	calls    map[string]int
	fail     string
	utilSeen map[string]int
}

func (f *spScopeFixture) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "ce.us-east-1.amazonaws.com" || req.Method != http.MethodPost {
		f.t.Errorf("unexpected fixture request: %s %s", req.Method, req.URL)
		return nil, fmt.Errorf("unexpected fixture request")
	}
	var input struct {
		Filter    json.RawMessage
		NextToken string
	}
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		return nil, err
	}
	status, body := http.StatusOK, ""
	switch req.Header.Get("X-Amz-Target") {
	case "AWSInsightsIndexService.GetSavingsPlansCoverage":
		scope := "global"
		if len(input.Filter) > 0 {
			scope = "regional"
			assert.JSONEq(f.t, `{"Dimensions":{"Key":"REGION","Values":["us-east-1"]}}`, string(input.Filter))
		}
		f.calls[scope]++
		if scope == f.fail {
			status, body = http.StatusBadRequest, `{"__type":"InvalidParameterValueException","Message":"fixture coverage unavailable"}`
			break
		}
		page := 0
		if input.NextToken != "" {
			assert.Equal(f.t, "second", input.NextToken)
			page = 1
		}
		if page >= len(f.pages[scope]) {
			f.t.Errorf("unexpected %s coverage page %d", scope, page)
			return nil, fmt.Errorf("unexpected coverage page")
		}
		body = f.pages[scope][page]
	case "AWSInsightsIndexService.GetSavingsPlansUtilization":
		filter := string(input.Filter)
		if strings.Contains(filter, "EC2InstanceSavingsPlans") {
			assert.JSONEq(f.t, `{"And":[{"Dimensions":{"Key":"SAVINGS_PLANS_TYPE","Values":["EC2InstanceSavingsPlans"]}},{"Dimensions":{"Key":"REGION","Values":["us-east-1"]}}]}`, filter)
			f.utilSeen["regional"]++
			body = `{"Total":{"Utilization":{"UtilizationPercentage":"81"}}}`
		} else {
			assert.JSONEq(f.t, `{"Dimensions":{"Key":"SAVINGS_PLANS_TYPE","Values":["ComputeSavingsPlans"]}}`, filter)
			f.utilSeen["global"]++
			body = `{"Total":{"Utilization":{"UtilizationPercentage":"92"}}}`
		}
	default:
		f.t.Errorf("unexpected fixture operation: %s", req.Header.Get("X-Amz-Target"))
		return nil, fmt.Errorf("unexpected fixture operation")
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func spScopeCoveragePage(covered, uncovered, token string) string {
	return fmt.Sprintf(`{"NextToken":%q,"SavingsPlansCoverages":[{"Coverage":{"SpendCoveredBySavingsPlans":%q,"OnDemandCost":%q}}]}`, token, covered, uncovered)
}

func TestGetLayerStates_SPCoverageScopeSDK(t *testing.T) {
	for _, tc := range []struct {
		name         string
		regional     []string
		global       []string
		fail         string
		wantRegional *float64
		wantGlobal   *float64
	}{
		{"distinct_geographies", []string{spScopeCoveragePage("5", "95", "")}, []string{spScopeCoveragePage("900", "100", "")}, "", ptr(5), ptr(90)},
		{"global_pagination", []string{spScopeCoveragePage("5", "95", "")}, []string{spScopeCoveragePage("400", "100", "second"), spScopeCoveragePage("500", "0", "")}, "", ptr(5), ptr(90)},
		{"equal_percentages", []string{spScopeCoveragePage("5", "95", "")}, []string{spScopeCoveragePage("50", "950", "")}, "", ptr(5), ptr(5)},
		{"zero_with_eligible_spend", []string{spScopeCoveragePage("0", "100", "")}, []string{spScopeCoveragePage("0", "1000", "")}, "", ptr(0), ptr(0)},
		{"no_data", []string{`{}`}, []string{`{}`}, "", nil, nil},
		{"global_failure", []string{spScopeCoveragePage("5", "95", "")}, nil, "global", ptr(5), nil},
		{"regional_failure", nil, []string{spScopeCoveragePage("900", "100", "")}, "regional", nil, ptr(90)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &spScopeFixture{t: t, pages: map[string][]string{"regional": tc.regional, "global": tc.global},
				calls: make(map[string]int), fail: tc.fail, utilSeen: make(map[string]int)}
			client := recommendations.NewClient(&aws.Config{
				Region: "us-east-1", HTTPClient: fixture, RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
				}),
			})
			cov := &fakeCoverageSource{}
			a, err := New(Config{Region: "us-east-1", AccountID: "123456789012", HorizonDays: 30, LookbackDays: 30},
				&fakeRILister{}, &fakeSPLister{}, cov, cov, &fakeUtilizationSource{},
				&spCoverageAdapter{client: client}, &spUtilizationAdapter{client: client})
			require.NoError(t, err)
			states, err := a.GetLayerStates(context.Background(), testScope())
			require.NoError(t, err)
			for layer, want := range map[ladder.LayerType]*float64{ladder.LayerEC2InstanceSP: tc.wantRegional, ladder.LayerComputeSP: tc.wantGlobal} {
				got := states[layer].CoveragePct
				if want == nil {
					assert.Nil(t, got, "%s aggregate SP coverage", layer)
				} else if assert.NotNil(t, got, "%s aggregate SP coverage", layer) {
					assert.InDelta(t, *want, *got, 1e-9, "%s aggregate SP coverage", layer)
				}
			}
			assert.Equal(t, ptr(81), states[ladder.LayerEC2InstanceSP].UtilizationPct)
			assert.Equal(t, ptr(92), states[ladder.LayerComputeSP].UtilizationPct)
			assert.Equal(t, map[string]int{"regional": 1, "global": 1}, fixture.utilSeen)
			assert.Equal(t, 1, fixture.calls["regional"])
			assert.Equal(t, max(1, len(tc.global)), fixture.calls["global"])
		})
	}
}
