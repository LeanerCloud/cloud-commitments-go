package savingsplans

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
)

type fakeCred struct{ err error }

func (f fakeCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "tok"}, f.err
}

type routeHTTP struct {
	reqs  []*http.Request
	reply func(r *http.Request) (int, string)
}

func (h *routeHTTP) Do(r *http.Request) (*http.Response, error) {
	h.reqs = append(h.reqs, r)
	code, body := h.reply(r)
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// Fixture-based: shape follows the documented 2024-08-01 response
// (benefitRecommendationModel, sharedScopeBenefitRecommendationProperties).
// The live probe of the test subscription returned {"value": []}.
func benefitRow(term string, mutate func(string) string) string {
	row := `{"id":"/subscriptions/sub-1/providers/Microsoft.CostManagement/benefitRecommendations/r-` + term + `",
"name":"r","type":"Microsoft.CostManagement/benefitRecommendations","kind":"SavingsPlan",
"properties":{"firstConsumptionDate":"2026-08-01T00:00:00Z","lastConsumptionDate":"2026-10-01T00:00:00Z",
"lookBackPeriod":"Last60Days","totalHours":1460,"armSkuName":"Compute_Savings_Plan","term":"` + term + `",
"commitmentGranularity":"Hourly","currencyCode":"USD","costWithoutBenefit":2920,"scope":"Shared",
"recommendationDetails":{"commitmentAmount":1.5,"savingsAmount":292,"savingsPercentage":10,"totalCost":2628}}}`
	if mutate != nil {
		row = mutate(row)
	}
	return row
}

func TestHoursPerMonthIs730(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 730.0, float64(hoursPerMonth), 0)
	assert.InDelta(t, 8760.0, float64(hoursPerMonth)*12, 0)
}

func newTestClient(h HTTPClient) *Client { return NewClientWithHTTP(fakeCred{}, "sub-1", "", h) }

func TestGetRecommendations_MapsRow(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(*http.Request) (int, string) {
		return 200, `{"value":[` + benefitRow("P1Y", nil) + `]}`
	}}
	recs, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "1yr", LookbackPeriod: "30d"})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	r := recs[0]
	assert.Equal(t, "1yr", r.Term)
	assert.Equal(t, "monthly", r.PaymentOption)
	assert.Equal(t, 0.0, r.CommitmentCost)
	require.NotNil(t, r.RecurringMonthlyCost)
	assert.InDelta(t, 1.5*hoursPerMonth, *r.RecurringMonthlyCost, 1e-9)
	assert.InDelta(t, 292*hoursPerMonth/1460, r.EstimatedSavings, 1e-9)
	assert.InDelta(t, 2920*hoursPerMonth/1460, r.OnDemandCost, 1e-9)
	d := r.Details.(*common.SavingsPlanDetails)
	assert.Equal(t, 1.5, d.HourlyCommitment)
	assert.Equal(t, "USD", d.Currency)
	assert.Equal(t, common.ServiceSavingsPlansAll, r.Service)

	require.Len(t, h.reqs, 1)
	q := h.reqs[0].URL.Query()
	assert.Equal(t, "2024-08-01", q.Get("api-version"))
	assert.Equal(t, "properties/scope eq 'Shared' AND properties/term eq 'P1Y' AND properties/lookBackPeriod eq 'Last30Days'", q.Get("$filter"))
	assert.Equal(t, "Bearer tok", h.reqs[0].Header.Get("Authorization"))
}

func TestGetRecommendations_EmptyTermRequestsBoth(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(r *http.Request) (int, string) {
		term := "P1Y"
		if strings.Contains(r.URL.RawQuery, "P3Y") || strings.Contains(r.URL.Query().Get("$filter"), "P3Y") {
			term = "P3Y"
		}
		return 200, `{"value":[` + benefitRow(term, nil) + `]}`
	}}
	recs, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, "1yr", recs[0].Term)
	assert.Equal(t, "3yr", recs[1].Term)
	assert.Contains(t, h.reqs[0].URL.Query().Get("$filter"), "properties/lookBackPeriod eq 'Last7Days'")
}

func TestGetRecommendations_EmptyValue(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(*http.Request) (int, string) { return 200, `{"value":[]}` }}
	recs, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "3yr"})
	require.NoError(t, err)
	assert.Empty(t, recs)
}

func TestGetRecommendations_Pagination(t *testing.T) {
	t.Parallel()
	next := "https://management.azure.com/next?page=2"
	h := &routeHTTP{reply: func(r *http.Request) (int, string) {
		if r.URL.Query().Get("page") == "2" {
			return 200, `{"value":[` + benefitRow("P3Y", nil) + `]}`
		}
		return 200, `{"value":[` + benefitRow("P3Y", nil) + `],"nextLink":"` + next + `"}`
	}}
	recs, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "3yr"})
	require.NoError(t, err)
	assert.Len(t, recs, 2)
}

func TestGetRecommendations_RejectsForeignNextLink(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(*http.Request) (int, string) {
		return 200, `{"value":[],"nextLink":"https://evil.example/steal"}`
	}}
	_, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "3yr"})
	require.Error(t, err)
	assert.Len(t, h.reqs, 1, "bearer token must not be sent to the foreign host")
}

func TestGetRecommendations_RejectsHTTPNextLink(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(*http.Request) (int, string) {
		return 200, `{"value":[],"nextLink":"http://management.azure.com/next"}`
	}}
	_, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "3yr"})
	require.Error(t, err)
	assert.Len(t, h.reqs, 1)
}

func TestGetRecommendations_Errors(t *testing.T) {
	t.Parallel()
	ok := func(*http.Request) (int, string) { return 200, `{"value":[` + benefitRow("P1Y", nil) + `]}` }
	cases := []struct {
		name   string
		params common.RecommendationParams
		reply  func(*http.Request) (int, string)
		cred   error
	}{
		{"http 403", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) { return 403, `{"error":{"code":"AuthorizationFailed"}}` }, nil},
		{"bad json", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) { return 200, `{` }, nil},
		{"bad term", common.RecommendationParams{Term: "5yr"}, ok, nil},
		{"bad lookback", common.RecommendationParams{Term: "1yr", LookbackPeriod: "90d"}, ok, nil},
		{"token error", common.RecommendationParams{Term: "1yr"}, ok, errors.New("no token")},
		{"unknown term", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) {
			return 200, `{"value":[` + benefitRow("P5Y", nil) + `]}`
		}, nil},
		{"unknown sku", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) {
			return 200, `{"value":[` + benefitRow("P1Y", func(s string) string { return strings.Replace(s, "Compute_Savings_Plan", "Other", 1) }) + `]}`
		}, nil},
		{"single scope", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) {
			return 200, `{"value":[` + benefitRow("P1Y", func(s string) string { return strings.Replace(s, `"Shared"`, `"Single"`, 1) }) + `]}`
		}, nil},
		{"missing commitment", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) {
			return 200, `{"value":[` + benefitRow("P1Y", func(s string) string { return strings.Replace(s, `"commitmentAmount":1.5,`, "", 1) }) + `]}`
		}, nil},
		{"zero hours", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) {
			return 200, `{"value":[` + benefitRow("P1Y", func(s string) string { return strings.Replace(s, `"totalHours":1460`, `"totalHours":0`, 1) }) + `]}`
		}, nil},
		{"bad currency", common.RecommendationParams{Term: "1yr"}, func(*http.Request) (int, string) {
			return 200, `{"value":[` + benefitRow("P1Y", func(s string) string { return strings.Replace(s, `"USD"`, `""`, 1) }) + `]}`
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClientWithHTTP(fakeCred{err: tc.cred}, "sub-1", "", &routeHTTP{reply: tc.reply})
			recs, err := c.GetRecommendations(context.Background(), &tc.params)
			require.Error(t, err)
			assert.Nil(t, recs)
		})
	}
}

func TestGetRecommendations_NoCredential(t *testing.T) {
	t.Parallel()
	_, err := NewClient(nil, "sub", "").GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.Error(t, err)
}

func TestBenefitRecsURL_PathAndQuery(t *testing.T) {
	t.Parallel()
	u, err := url.Parse(newTestClient(nil).benefitRecsURL("P3Y", ""))
	require.NoError(t, err)
	assert.Equal(t, "/subscriptions/sub-1/providers/Microsoft.CostManagement/benefitRecommendations", u.Path)
}

func TestGetRecommendations_PageCapIsAnError(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(*http.Request) (int, string) {
		return 200, `{"value":[],"nextLink":"https://management.azure.com/loop"}`
	}}
	_, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "3yr"})
	require.Error(t, err)
	assert.Len(t, h.reqs, benefitRecsMaxPages)
}

func TestGetRecommendations_NonUSDRowIsSkippedAndSurfaced(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		currency string
		wantSkip bool
	}{
		{"USD kept", "USD", false},
		{"EUR skipped", "EUR", true},
		{"GBP skipped", "GBP", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := benefitRow("P1Y", func(s string) string {
				s = strings.Replace(s, `"USD"`, `"`+tc.currency+`"`, 1)
				return strings.Replace(s, "r-P1Y", "r-bad", 1)
			})
			good := benefitRow("P1Y", func(s string) string { return strings.Replace(s, "r-P1Y", "r-good", 1) })
			h := &routeHTTP{reply: func(*http.Request) (int, string) {
				return 200, `{"value":[` + bad + `,` + good + `]}`
			}}
			ctx, skips := recommendations.WithSkipLog(context.Background())
			recs, err := newTestClient(h).GetRecommendations(ctx, &common.RecommendationParams{Term: "1yr"})
			require.NoError(t, err)
			if !tc.wantSkip {
				assert.Len(t, recs, 2)
				assert.Empty(t, skips.Entries())
				return
			}
			require.Len(t, recs, 1)
			assert.Contains(t, recs[0].SourceRecommendation, "r-good")
			entries := skips.Entries()
			require.Len(t, entries, 1)
			assert.Contains(t, entries[0], "savingsplans")
			assert.Contains(t, entries[0], "r-bad")
			assert.Contains(t, entries[0], tc.currency)
			assert.Contains(t, entries[0], recommendations.ErrNotUSD.Error())
		})
	}
}

func TestConvertBenefitRecommendation_NonUSDWrapsSentinel(t *testing.T) {
	t.Parallel()
	var row benefitRecommendation
	require.NoError(t, json.Unmarshal([]byte(strings.Replace(benefitRow("P1Y", nil), `"USD"`, `"EUR"`, 1)), &row))
	rec, err := convertBenefitRecommendation(&row, "sub-1")
	assert.Nil(t, rec)
	assert.ErrorIs(t, err, recommendations.ErrNotUSD)
}
