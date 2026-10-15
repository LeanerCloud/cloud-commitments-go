package ladder_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"
)

type ceLoopback struct {
	endpoint  *url.URL
	transport http.RoundTripper
}

func (c ceLoopback) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "ce.us-east-1.amazonaws.com" {
		err := fmt.Errorf("fixture rejects host %s", req.URL.Host)
		if req.Body != nil {
			err = errors.Join(err, req.Body.Close())
		}
		return nil, err
	}
	local := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		payload, readErr := io.ReadAll(req.Body)
		if err := errors.Join(readErr, req.Body.Close()); err != nil {
			return nil, fmt.Errorf("fixture request body: %w", err)
		}
		local.Body = io.NopCloser(bytes.NewReader(payload))
	}
	local.URL.Scheme, local.URL.Host = c.endpoint.Scheme, c.endpoint.Host
	local.Host = c.endpoint.Host
	return c.transport.RoundTrip(local)
}

// These CE-shaped responses are constructed fixtures, not recorded billing data.
func rateDetail(account, region, family, cost, savings string) string {
	return fmt.Sprintf(`{"AccountId":%q,"SavingsPlansDetails":{"Region":%q,"InstanceFamily":%q},"EstimatedSPCost":%q,"EstimatedSavingsAmount":%q,"EstimatedAverageUtilization":"100"}`,
		account, region, family, cost, savings)
}

func ratePage(details, token string) string {
	return fmt.Sprintf(`{"NextPageToken":%q,"SavingsPlansPurchaseRecommendation":{"SavingsPlansPurchaseRecommendationDetails":[%s]}}`, token, details)
}

func sdkRateLadder(t *testing.T, pages []string) *awsladder.AWSLadder {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := int(calls.Add(1)) - 1
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation", r.Header.Get("X-Amz-Target"))
		var input map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		want := map[string]any{"SavingsPlansType": "EC2_INSTANCE_SP", "AccountScope": "LINKED",
			"TermInYears": "THREE_YEARS", "PaymentOption": "PARTIAL_UPFRONT", "LookbackPeriodInDays": "SEVEN_DAYS"}
		if page > 0 {
			want["NextPageToken"] = "second"
		}
		assert.Equal(t, want, input)
		if page >= len(pages) {
			t.Error("unexpected continuation request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, err := w.Write([]byte(pages[page]))
		assert.NoError(t, err)
	}))
	t.Cleanup(func() {
		srv.Close()
		assert.Equal(t, int32(len(pages)), calls.Load())
	})
	endpoint, err := url.Parse(srv.URL)
	require.NoError(t, err)
	awsCfg := aws.Config{Region: "us-west-2", RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""),
		HTTPClient:  &http.Client{Transport: ceLoopback{endpoint: endpoint, transport: srv.Client().Transport}},
	}
	a, err := awsladder.NewFromConfig(awsCfg, awsladder.Config{Region: "eu-west-1", AccountID: "fixture-account", LookbackDays: 7})
	require.NoError(t, err)
	assert.Equal(t, "us-west-2", awsCfg.Region)
	return a
}

func TestCommitmentRates_PublicSDKConsumer(t *testing.T) {
	a := sdkRateLadder(t, []string{
		ratePage(rateDetail("fixture-account", "eu-west-1", "m5", "10", "10")+","+
			rateDetail("other-fixture-account", "eu-west-1", "m5", "999", "1")+","+
			rateDetail("fixture-account", "us-east-1", "m5", "999", "1"), "second"),
		ratePage(rateDetail("fixture-account", "eu-west-1", "c5", "60", "20"), ""),
	})
	scope := ladder.Scope{Provider: common.ProviderAWS, AccountID: "fixture-account"}
	rates, err := a.GetCommitmentRates(context.Background(), scope, ladder.Term3Year, ladder.PaymentPartialUpfront)
	require.NoError(t, err)
	require.Equal(t, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7}, rates)
	in := consumerInput(a, scope, rates)
	result, err := ladder.Allocate(in)
	require.NoError(t, err)
	require.Len(t, result.Allocations, 1)
	allocation := result.Allocations[0]
	assert.Equal(t, ladder.LayerEC2InstanceSP, allocation.Layer)
	assert.Equal(t, 0, allocation.OnDemandEquivalentUSDPerHour.Cmp(big.NewRat(10, 1)))
	assert.InDelta(t, 7, ratFloat(allocation.GapUSDPerHour), 1e-12)
	assertMissingRateHold(t, result, ladder.LayerComputeSP)
	tranches, err := ladder.BuildTranches(&ladder.TrancheInput{
		Config: &in.Config, RunID: "synthetic-run", Term: ladder.Term3Year,
		PaymentOption: ladder.PaymentPartialUpfront, NewID: func() string { return "synthetic-tranche" },
		Now: in.Now, Allocations: result.Allocations,
	})
	require.NoError(t, err)
	require.Len(t, tranches.Tranches, 1)
	tr := tranches.Tranches[0]
	assert.Equal(t, allocation.GapUSDPerHour.RatString(), tr.AmountUSDPerHour)
	assert.Equal(t, allocation.Rate.RatString(), tr.CommitmentRate)
	storedAmount, ok := new(big.Rat).SetString(tr.AmountUSDPerHour)
	require.True(t, ok)
	storedRate, ok := new(big.Rat).SetString(tr.CommitmentRate)
	require.True(t, ok)
	in.InFlight = []ladder.InFlightTranche{{Layer: tr.Layer, CommitmentUSDPerHour: ratFloat(storedAmount), Rate: ratFloat(storedRate)}}
	replay, err := ladder.Allocate(in)
	require.NoError(t, err)
	assert.True(t, replay.IsNoOp())
	assertMissingRateHold(t, replay, ladder.LayerComputeSP)
}

func consumerInput(a *awsladder.AWSLadder, scope ladder.Scope, rates map[ladder.LayerType]float64) *ladder.AllocationInput {
	zero, low, stable := 0.0, 20.0, 10.0
	states := make(map[ladder.LayerType]ladder.LayerState)
	for _, spec := range a.SupportedLayers() {
		states[spec.Type] = ladder.LayerState{Layer: spec.Type, ExistingUSDPerHour: &zero, ExpiringUSDPerHour: &zero}
	}
	return &ladder.AllocationInput{
		Config: ladder.LadderConfig{Scope: scope, Mode: ladder.ModeEmailApproval, Cadence: ladder.CadenceWeekly,
			TargetCoveragePct: 100, BaselinePercentile: 5, LookbackDays: 7, MaxActionsPerRun: 10,
			BufferUtilizationThresholdPct: ladder.DefaultBufferUtilizationThresholdPct,
			Ramp:                          ladder.RampSchedule{Steps: []ladder.RampStep{{AfterDays: 0, Fraction: 1}}}},
		Layers: a.SupportedLayers(), LayerStates: states, CommitmentRates: rates, InFlight: []ladder.InFlightTranche{},
		Baseline: ladder.UsageBaseline{LowWaterUSDPerHour: &low, StableUSDPerHour: &stable},
		Now:      time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), DataSources: []string{"constructed-ce-loopback"},
	}
}

func ratFloat(r *big.Rat) float64 { f, _ := r.Float64(); return f }

func assertMissingRateHold(t *testing.T, result *ladder.AllocateResult, layer ladder.LayerType) {
	t.Helper()
	for _, hold := range result.Holds {
		if hold.Layer == layer && hold.Action == ladder.ActionHold {
			assert.Contains(t, hold.Rationale, "no commitment rate")
			return
		}
	}
	t.Errorf("missing rate Hold for %s: %+v", layer, result)
}

func TestCommitmentRates_SDKMissingAndMalformed(t *testing.T) {
	for _, cost := range []string{"", "bad"} {
		t.Run("cost="+cost, func(t *testing.T) {
			detail := rateDetail("fixture-account", "eu-west-1", "m5", cost, "3")
			if cost == "" {
				detail = `{"AccountId":"fixture-account","SavingsPlansDetails":{"Region":"eu-west-1"}}`
			}
			a := sdkRateLadder(t, []string{ratePage(detail, "")})
			scope := ladder.Scope{Provider: common.ProviderAWS, AccountID: "fixture-account"}
			rates, err := a.GetCommitmentRates(context.Background(), scope, ladder.Term3Year, ladder.PaymentPartialUpfront)
			if cost != "" {
				require.ErrorContains(t, err, "EstimatedSPCost")
				assert.Nil(t, rates)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, rates)
			assert.Empty(t, rates)
			result, err := ladder.Allocate(consumerInput(a, scope, rates))
			require.NoError(t, err)
			assert.True(t, result.IsNoOp())
			assertMissingRateHold(t, result, ladder.LayerEC2InstanceSP)
		})
	}
}
