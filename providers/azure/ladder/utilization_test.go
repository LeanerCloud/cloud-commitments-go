package ladder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCred struct{}

func (fakeCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "tok", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

const planA = "/providers/Microsoft.BillingBenefits/savingsPlanOrders/o1/savingsPlans/pa"

const fixture = `{"value":[
 {"properties":{"benefitId":"` + planA + `","avgUtilizationPercentage":90}},
 {"properties":{"benefitId":"` + planA + `","avgUtilizationPercentage":70}}
]}`

func newReader(t *testing.T, h http.HandlerFunc) *UtilizationReader {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	r := NewUtilizationReader(fakeCred{})
	r.baseURL = srv.URL
	return r
}

func window() (time.Time, time.Time) {
	return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
}

func TestGetSavingsPlanUtilization_DecodesAndAverages(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/providers/microsoft.BillingBenefits/savingsPlanOrders/o1/providers/Microsoft.CostManagement/benefitUtilizationSummaries", req.URL.Path)
		assert.Equal(t, "2026-06-01", req.URL.Query().Get("api-version"))
		assert.Equal(t, "Daily", req.URL.Query().Get("grainParameter"))
		assert.Equal(t, "properties/usageDate ge 2026-10-01 and properties/usageDate le 2026-10-31", req.URL.Query().Get("$filter"))
		assert.Equal(t, "Bearer tok", req.Header.Get("Authorization"))
		_, _ = w.Write([]byte(fixture))
	})
	s, e := window()
	got, err := r.GetSavingsPlanUtilization(context.Background(), []string{"o1"}, s, e)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{planA: 80}, got)
}

func TestGetSavingsPlanUtilization_EmptyIsValid(t *testing.T) {
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"value":[]}`)) })
	s, e := window()
	got, err := r.GetSavingsPlanUtilization(context.Background(), []string{"o1"}, s, e)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGetSavingsPlanUtilization_Errors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"non-200":      {http.StatusForbidden, `{"error":{"code":"AuthorizationFailed"}}`},
		"decode error": {http.StatusOK, `{not json`},
		"missing pct":  {http.StatusOK, `{"value":[{"properties":{"benefitId":"p"}}]}`},
		"over 100":     {http.StatusOK, `{"value":[{"properties":{"benefitId":"p","avgUtilizationPercentage":100.5}}]}`},
		"negative":     {http.StatusOK, `{"value":[{"properties":{"benefitId":"p","avgUtilizationPercentage":-1}}]}`},
		"missing id":   {http.StatusOK, `{"value":[{"properties":{"avgUtilizationPercentage":50}}]}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			s, e := window()
			got, err := r.GetSavingsPlanUtilization(context.Background(), []string{"o1"}, s, e)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestGetSavingsPlanUtilization_RejectsForeignNextLink(t *testing.T) {
	var foreignHits atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignHits.Add(1)
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	t.Cleanup(foreign.Close)
	r := newReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":[],"nextLink":"` + foreign.URL + `/x"}`))
	})
	s, e := window()
	_, err := r.GetSavingsPlanUtilization(context.Background(), []string{"o1"}, s, e)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nextLink outside")
	assert.Zero(t, foreignHits.Load())
}
