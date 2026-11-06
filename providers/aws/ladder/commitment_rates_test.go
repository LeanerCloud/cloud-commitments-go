package ladder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type fakeRateResult struct {
	rate float64
	err  error
}

// fakeSPRateSource answers per plan type; an unlisted plan type is a test bug.
type fakeSPRateSource struct {
	results map[types.SupportedSavingsPlansType]fakeRateResult
	params  []recommendations.SPCommitmentRateParams
}

// ec2Fake answers EC2 Instance SP with the given result and Compute SP with a
// fixed usable rate of 0.8.
func ec2Fake(rate float64, err error) *fakeSPRateSource {
	return layersFake(fakeRateResult{rate, err}, fakeRateResult{rate: 0.8})
}

func layersFake(ec2, compute fakeRateResult) *fakeSPRateSource {
	return &fakeSPRateSource{results: map[types.SupportedSavingsPlansType]fakeRateResult{
		types.SupportedSavingsPlansTypeEc2InstanceSp: ec2,
		types.SupportedSavingsPlansTypeComputeSp:     compute,
	}}
}

func TestNewFromConfig_ValidatesAndScopesClients(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{{AccountID: testScope().AccountID}, {Region: "eu-west-1"}} {
		a, err := NewFromConfig(aws.Config{}, cfg)
		require.Error(t, err)
		assert.Nil(t, a)
	}
	awsCfg := aws.Config{Region: "us-west-2"}
	a, err := NewFromConfig(awsCfg, Config{Region: "eu-west-1", AccountID: testScope().AccountID})
	require.NoError(t, err)
	assert.Equal(t, "us-west-2", awsCfg.Region)
	assert.Equal(t, "eu-west-1", a.cfg.Region)
	assert.Equal(t, "eu-west-1", a.sps.(*spListerAdapter).region)
	require.NotNil(t, a.spRates)
}

func (f *fakeSPRateSource) GetSPCommitmentRate(_ context.Context, p recommendations.SPCommitmentRateParams) (float64, error) {
	f.params = append(f.params, p)
	r, ok := f.results[p.PlanType]
	if !ok {
		return 0, fmt.Errorf("unexpected plan type %s", p.PlanType)
	}
	return r.rate, r.err
}

func TestGetCommitmentRates_ValidatesBeforeSource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		scope   ladder.Scope
		term    ladder.Term
		payment ladder.PaymentOption
	}{
		{"provider", ladder.Scope{Provider: common.ProviderAzure, AccountID: testScope().AccountID}, ladder.Term1Year, ladder.PaymentNoUpfront},
		{"account", ladder.Scope{Provider: common.ProviderAWS, AccountID: "wrong"}, ladder.Term1Year, ladder.PaymentNoUpfront},
		{"term", testScope(), ladder.Term("bad"), ladder.PaymentNoUpfront},
		{"payment", testScope(), ladder.Term1Year, ladder.PaymentOption("bad")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ec2Fake(0.7, nil)
			a := &AWSLadder{cfg: Config{Region: "us-east-1", AccountID: testScope().AccountID}, spRates: f}
			rates, err := a.GetCommitmentRates(context.Background(), tc.scope, tc.term, tc.payment)
			require.Error(t, err)
			assert.Nil(t, rates)
			assert.Empty(t, f.params)
		})
	}
}

func TestGetCommitmentRates_ForwardsSelection(t *testing.T) {
	t.Parallel()
	// Every requested term and payment must reach Cost Explorer as is, for both
	// layers: a 1yr no-upfront ladder sized at a 3yr rate would over-buy.
	selections := []struct {
		term    ladder.Term
		payment ladder.PaymentOption
		wantT   string
		wantP   string
	}{
		{ladder.Term1Year, ladder.PaymentNoUpfront, "1yr", "no-upfront"},
		{ladder.Term3Year, ladder.PaymentAllUpfront, "3yr", "all-upfront"},
		{ladder.Term3Year, ladder.PaymentPartialUpfront, "3yr", "partial-upfront"},
	}
	for _, sel := range selections {
		for _, days := range []int{0, 7, 60} {
			t.Run(fmt.Sprintf("%s/%s/%d", sel.wantT, sel.wantP, days), func(t *testing.T) {
				f := ec2Fake(0.7, nil)
				a := &AWSLadder{cfg: Config{Region: "eu-west-1", AccountID: testScope().AccountID, LookbackDays: days}, spRates: f}
				rates, err := a.GetCommitmentRates(context.Background(), testScope(), sel.term, sel.payment)
				require.NoError(t, err)
				assert.Equal(t, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7, ladder.LayerComputeSP: 0.8}, rates)
				wantDays := days
				if wantDays == 0 {
					wantDays = DefaultLookbackDays
				}
				base := recommendations.SPCommitmentRateParams{
					AccountID: testScope().AccountID, Region: "eu-west-1",
					Term: sel.wantT, PaymentOption: sel.wantP, LookbackPeriod: fmt.Sprintf("%dd", wantDays),
				}
				ec2, compute := base, base
				ec2.PlanType = types.SupportedSavingsPlansTypeEc2InstanceSp
				compute.PlanType = types.SupportedSavingsPlansTypeComputeSp
				assert.Equal(t, []recommendations.SPCommitmentRateParams{ec2, compute}, f.params)
			})
		}
	}
}

func TestGetCommitmentRates_AbsenceAndErrors(t *testing.T) {
	t.Parallel()
	hard := errors.New("source failed")
	for _, tc := range []struct {
		name   string
		source spCommitmentRateSource
		want   error
	}{
		{"unwired", nil, nil},
		{"absent", layersFake(fakeRateResult{err: fmt.Errorf("wrapped: %w", recommendations.ErrNoSPCommitmentRate)}, fakeRateResult{err: recommendations.ErrNoSPCommitmentRate}), nil},
		{"hard", ec2Fake(0, hard), hard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestLadder(t, &fakeRILister{}, &fakeSPLister{}, &fakeCoverageSource{}, &fakeUtilizationSource{})
			a.spRates = tc.source
			rates, err := a.GetCommitmentRates(context.Background(), testScope(), ladder.Term1Year, ladder.PaymentNoUpfront)
			if tc.source == nil {
				require.ErrorContains(t, err, "not wired")
				assert.Nil(t, rates)
			} else if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				assert.Nil(t, rates)
			} else {
				require.NoError(t, err)
				require.NotNil(t, rates)
				assert.Empty(t, rates)
			}
		})
	}
}

func TestGetCommitmentRates_RejectsUnusableRate(t *testing.T) {
	t.Parallel()
	for name, rate := range map[string]float64{"nan": math.NaN(), "inf": math.Inf(1), "zero": 0, "negative": -0.3} {
		t.Run(name, func(t *testing.T) {
			a := &AWSLadder{cfg: Config{Region: "us-east-1", AccountID: testScope().AccountID}, spRates: ec2Fake(rate, nil)}
			rates, err := a.GetCommitmentRates(context.Background(), testScope(), ladder.Term1Year, ladder.PaymentNoUpfront)
			require.ErrorContains(t, err, string(ladder.LayerEC2InstanceSP))
			assert.Nil(t, rates)
		})
	}
}

func TestGetCommitmentRates_HardErrorNamesLayer(t *testing.T) {
	t.Parallel()
	hard := errors.New("source failed")
	a := &AWSLadder{cfg: Config{Region: "us-east-1", AccountID: testScope().AccountID}, spRates: ec2Fake(0, hard)}
	_, err := a.GetCommitmentRates(context.Background(), testScope(), ladder.Term1Year, ladder.PaymentNoUpfront)
	require.ErrorIs(t, err, hard)
	assert.ErrorContains(t, err, string(ladder.LayerEC2InstanceSP))
}

func TestGetCommitmentRates_ComputeLayer(t *testing.T) {
	t.Parallel()
	missing := fmt.Errorf("none: %w", recommendations.ErrNoSPCommitmentRate)
	hard := errors.New("source failed")
	ec2, compute := fakeRateResult{rate: 0.7}, fakeRateResult{rate: 0.8}
	for _, tc := range []struct {
		name    string
		ec2     fakeRateResult
		compute fakeRateResult
		want    map[ladder.LayerType]float64
		wantErr error
	}{
		{"both", ec2, compute, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7, ladder.LayerComputeSP: 0.8}, nil},
		{"ec2 missing", fakeRateResult{err: missing}, compute, map[ladder.LayerType]float64{ladder.LayerComputeSP: 0.8}, nil},
		{"compute missing", ec2, fakeRateResult{err: missing}, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7}, nil},
		{"compute hard", ec2, fakeRateResult{err: hard}, nil, hard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := layersFake(tc.ec2, tc.compute)
			a := &AWSLadder{cfg: Config{Region: "eu-west-1", AccountID: testScope().AccountID}, spRates: f}
			got, err := a.GetCommitmentRates(context.Background(), testScope(), ladder.Term1Year, ladder.PaymentNoUpfront)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.ErrorContains(t, err, string(ladder.LayerComputeSP))
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFetchLayerRates_LayersAreIndependent(t *testing.T) {
	t.Parallel()
	const (
		planA = types.SupportedSavingsPlansTypeEc2InstanceSp
		planB = types.SupportedSavingsPlansTypeComputeSp
	)
	layers := []rateLayer{
		{layer: ladder.LayerEC2InstanceSP, plan: planA, region: "eu-west-1"},
		{layer: ladder.LayerComputeSP, plan: planB, region: ""},
	}
	missing := fmt.Errorf("none: %w", recommendations.ErrNoSPCommitmentRate)
	hard := errors.New("source failed")
	for _, tc := range []struct {
		name    string
		a, b    fakeRateResult
		want    map[ladder.LayerType]float64
		wantErr error
	}{
		{"first missing second present", fakeRateResult{err: missing}, fakeRateResult{rate: 0.8}, map[ladder.LayerType]float64{ladder.LayerComputeSP: 0.8}, nil},
		{"first present second hard", fakeRateResult{rate: 0.7}, fakeRateResult{err: hard}, nil, hard},
		{"both missing", fakeRateResult{err: missing}, fakeRateResult{err: missing}, map[ladder.LayerType]float64{}, nil},
		{"both present", fakeRateResult{rate: 0.7}, fakeRateResult{rate: 0.8}, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7, ladder.LayerComputeSP: 0.8}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSPRateSource{results: map[types.SupportedSavingsPlansType]fakeRateResult{planA: tc.a, planB: tc.b}}
			got, err := fetchLayerRates(context.Background(), f, layers, recommendations.SPCommitmentRateParams{AccountID: "acct"})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			require.Len(t, f.params, 2)
			assert.Equal(t, "eu-west-1", f.params[0].Region)
			assert.Equal(t, planB, f.params[1].PlanType)
		})
	}
}
