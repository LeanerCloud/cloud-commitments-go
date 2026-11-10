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

func riRateDetail(account, region, recurring, savings string) string {
	return fmt.Sprintf(`{"AccountId":%q,"InstanceDetails":{"EC2InstanceDetails":{"Region":%q}},"RecurringStandardMonthlyCost":%q,"UpfrontCost":"0","EstimatedMonthlySavingsAmount":%q,"AverageUtilization":"100"}`,
		account, region, recurring, savings)
}

func riRatePage(details, token string) string {
	return fmt.Sprintf(`{"NextPageToken":%q,"Recommendations":[{"RecommendationDetails":[%s]}]}`, token, details)
}

const (
	spTarget = "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation"
	riTarget = "AWSInsightsIndexService.GetReservationPurchaseRecommendation"
)

// sdkRateLadder serves pages per Savings Plans type and for the Reservation
// recommendation (riPages, nil means one empty page), asserting each request
// and that exactly those pages are requested.
func sdkRateLadder(t *testing.T, pagesByType map[string][]string, riPages []string) *awsladder.AWSLadder {
	t.Helper()
	if riPages == nil {
		riPages = []string{"{}"}
	}
	calls := map[string]*atomic.Int32{"EC2_INSTANCE_SP": {}, "COMPUTE_SP": {}, "RI": {}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var input map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		want := map[string]any{"AccountScope": "LINKED",
			"TermInYears": "THREE_YEARS", "PaymentOption": "PARTIAL_UPFRONT", "LookbackPeriodInDays": "SEVEN_DAYS"}
		var key string
		var pages []string
		switch target := r.Header.Get("X-Amz-Target"); target {
		case spTarget:
			key, _ = input["SavingsPlansType"].(string)
			pages = pagesByType[key]
			want["SavingsPlansType"] = key
		case riTarget:
			key, pages = "RI", riPages
			want["Service"] = "Amazon Elastic Compute Cloud - Compute"
			want["ServiceSpecification"] = map[string]any{"EC2Specification": map[string]any{"OfferingClass": "CONVERTIBLE"}}
		default:
			assert.Fail(t, "unexpected operation", target)
		}
		counter, known := calls[key]
		if !assert.True(t, known, "unexpected request key %q", key) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		page := int(counter.Add(1)) - 1
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
		for key, counter := range calls {
			want := len(pagesByType[key])
			if key == "RI" {
				want = len(riPages)
			}
			assert.Equal(t, int32(want), counter.Load(), key)
		}
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
	t.Parallel()
	a := sdkRateLadder(t, map[string][]string{
		"EC2_INSTANCE_SP": {
			ratePage(rateDetail("fixture-account", "eu-west-1", "m5", "10", "10")+","+
				rateDetail("other-fixture-account", "eu-west-1", "m5", "999", "1")+","+
				rateDetail("fixture-account", "us-east-1", "m5", "999", "1"), "second"),
			ratePage(rateDetail("fixture-account", "eu-west-1", "c5", "60", "20"), ""),
		},
		// Compute SP is account-wide: both regions count, other accounts do not.
		// The regions have different ratios (0.5 and 0.75), so only the blend of
		// both gives 0.625; a regional filter would give 0.5.
		"COMPUTE_SP": {ratePage(rateDetail("fixture-account", "eu-west-1", "", "20", "20")+","+
			rateDetail("fixture-account", "us-east-1", "", "30", "10")+","+
			rateDetail("other-fixture-account", "eu-west-1", "", "999", "1"), "")},
	}, []string{
		// Region-filtered: the display name matches eu-west-1, other regions
		// and accounts do not. r = 60/100 across the two pages.
		riRatePage(riRateDetail("fixture-account", "EU (Ireland)", "30", "20")+","+
			riRateDetail("fixture-account", "US East (N. Virginia)", "999", "1")+","+
			riRateDetail("other-fixture-account", "EU (Ireland)", "999", "1"), "second"),
		riRatePage(riRateDetail("fixture-account", "EU (Ireland)", "30", "20"), ""),
	})
	scope := ladder.Scope{Provider: common.ProviderAWS, AccountID: "fixture-account"}
	rates, err := a.GetCommitmentRates(context.Background(), scope, ladder.Term3Year, ladder.PaymentPartialUpfront)
	require.NoError(t, err)
	require.Equal(t, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7, ladder.LayerComputeSP: 0.625, ladder.LayerConvertibleRI: 0.6}, rates)
	in := consumerInput(a, scope, rates)
	result, err := ladder.Allocate(in)
	require.NoError(t, err)
	// With both SP rates present, the base gap buys EC2 Instance SP at 0.7 and
	// the flex gap buys Compute SP at the account-wide 0.625 (#349 option A).
	// The buffer share is zero here; see TestCommitmentRates_SDKBufferAtRIRate.
	require.Len(t, result.Allocations, 2)
	byLayer := map[ladder.LayerType]ladder.Allocation{}
	for _, a := range result.Allocations {
		byLayer[a.Layer] = a
	}
	ec2, compute := byLayer[ladder.LayerEC2InstanceSP], byLayer[ladder.LayerComputeSP]
	assert.Equal(t, 0, ec2.OnDemandEquivalentUSDPerHour.Cmp(big.NewRat(10, 1)))
	assert.InDelta(t, 7, ratFloat(ec2.GapUSDPerHour), 1e-12)
	assert.Equal(t, 0, compute.OnDemandEquivalentUSDPerHour.Cmp(big.NewRat(10, 1)))
	assert.InDelta(t, 6.25, ratFloat(compute.GapUSDPerHour), 1e-12)
	nextID := 0
	tranches, err := ladder.BuildTranches(&ladder.TrancheInput{
		Config: &in.Config, RunID: "synthetic-run", Term: ladder.Term3Year, PaymentOption: ladder.PaymentPartialUpfront,
		NewID: func() string { nextID++; return fmt.Sprintf("synthetic-tranche-%d", nextID) },
		Now:   in.Now, Allocations: result.Allocations,
	})
	require.NoError(t, err)
	require.Len(t, tranches.Tranches, 2)
	in.InFlight = []ladder.InFlightTranche{}
	for _, tr := range tranches.Tranches {
		alloc := byLayer[tr.Layer]
		assert.Equal(t, alloc.GapUSDPerHour.RatString(), tr.AmountUSDPerHour, tr.Layer)
		assert.Equal(t, alloc.Rate.RatString(), tr.CommitmentRate, tr.Layer)
		storedAmount, ok := new(big.Rat).SetString(tr.AmountUSDPerHour)
		require.True(t, ok)
		storedRate, ok := new(big.Rat).SetString(tr.CommitmentRate)
		require.True(t, ok)
		in.InFlight = append(in.InFlight, ladder.InFlightTranche{Layer: tr.Layer, CommitmentUSDPerHour: ratFloat(storedAmount), Rate: ratFloat(storedRate)})
	}
	replay, err := ladder.Allocate(in)
	require.NoError(t, err)
	assert.True(t, replay.IsNoOp())
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
	t.Parallel()
	for _, cost := range []string{"", "bad"} {
		t.Run("cost="+cost, func(t *testing.T) {
			detail := rateDetail("fixture-account", "eu-west-1", "m5", cost, "3")
			if cost == "" {
				detail = `{"AccountId":"fixture-account","SavingsPlansDetails":{"Region":"eu-west-1"}}`
			}
			pages := map[string][]string{"EC2_INSTANCE_SP": {ratePage(detail, "")}}
			if cost == "" {
				pages["COMPUTE_SP"] = []string{ratePage(detail, "")}
			}
			// A hard SP error stops before the RI request; the missing-SP case
			// reaches the RI layer, which answers an empty page.
			var riPages []string
			if cost != "" {
				riPages = []string{}
			}
			a := sdkRateLadder(t, pages, riPages)
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
			assertMissingRateHold(t, result, ladder.LayerComputeSP)
		})
	}
}

func TestCommitmentRates_SDKRIMissingAndMalformed(t *testing.T) {
	t.Parallel()
	sp := map[string][]string{
		"EC2_INSTANCE_SP": {ratePage(rateDetail("fixture-account", "eu-west-1", "m5", "70", "30"), "")},
		"COMPUTE_SP":      {ratePage(rateDetail("fixture-account", "eu-west-1", "", "80", "20"), "")},
	}
	scope := ladder.Scope{Provider: common.ProviderAWS, AccountID: "fixture-account"}
	t.Run("no matching detail", func(t *testing.T) {
		a := sdkRateLadder(t, sp, []string{riRatePage(riRateDetail("fixture-account", "US East (N. Virginia)", "70", "30"), "")})
		rates, err := a.GetCommitmentRates(context.Background(), scope, ladder.Term3Year, ladder.PaymentPartialUpfront)
		require.NoError(t, err)
		assert.Equal(t, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7, ladder.LayerComputeSP: 0.8}, rates)
	})
	t.Run("malformed detail", func(t *testing.T) {
		a := sdkRateLadder(t, sp, []string{riRatePage(riRateDetail("fixture-account", "EU (Ireland)", "bad", "30"), "")})
		rates, err := a.GetCommitmentRates(context.Background(), scope, ladder.Term3Year, ladder.PaymentPartialUpfront)
		require.ErrorContains(t, err, string(ladder.LayerConvertibleRI))
		assert.Nil(t, rates)
	})
}

func TestCommitmentRates_SDKBufferAtRIRate(t *testing.T) {
	t.Parallel()
	a := sdkRateLadder(t, map[string][]string{
		"EC2_INSTANCE_SP": {ratePage(rateDetail("fixture-account", "eu-west-1", "m5", "70", "30"), "")},
		"COMPUTE_SP":      {ratePage(rateDetail("fixture-account", "eu-west-1", "", "80", "20"), "")},
	}, []string{riRatePage(riRateDetail("fixture-account", "EU (Ireland)", "60", "40"), "")})
	scope := ladder.Scope{Provider: common.ProviderAWS, AccountID: "fixture-account"}
	rates, err := a.GetCommitmentRates(context.Background(), scope, ladder.Term3Year, ladder.PaymentPartialUpfront)
	require.NoError(t, err)
	require.InDelta(t, 0.6, rates[ladder.LayerConvertibleRI], 1e-12)
	in := consumerInput(a, scope, rates)
	in.Config.BufferFraction = 0.5
	result, err := ladder.Allocate(in)
	require.NoError(t, err)
	for _, alloc := range result.Allocations {
		if alloc.Layer == ladder.LayerConvertibleRI {
			assert.InDelta(t, 0.6, ratFloat(alloc.Rate), 1e-12)
			assert.InDelta(t, 10, ratFloat(alloc.OnDemandEquivalentUSDPerHour), 1e-12)
			assert.InDelta(t, 6, ratFloat(alloc.GapUSDPerHour), 1e-12)
			return
		}
	}
	t.Errorf("no convertible-ri allocation: %+v", result)
}
