package ladder

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type fakeSPRateSource struct {
	err    error
	params []recommendations.SPCommitmentRateParams
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
	return 0.7, f.err
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
			f := &fakeSPRateSource{}
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
	for _, days := range []int{0, 7, 60} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			f := &fakeSPRateSource{}
			a := &AWSLadder{cfg: Config{Region: "eu-west-1", AccountID: testScope().AccountID, LookbackDays: days}, spRates: f}
			rates, err := a.GetCommitmentRates(context.Background(), testScope(), ladder.Term3Year, ladder.PaymentPartialUpfront)
			require.NoError(t, err)
			assert.Equal(t, map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.7}, rates)
			if days == 0 {
				days = DefaultLookbackDays
			}
			assert.Equal(t, []recommendations.SPCommitmentRateParams{{
				PlanType:  types.SupportedSavingsPlansTypeEc2InstanceSp,
				AccountID: testScope().AccountID, Region: "eu-west-1",
				Term: "3yr", PaymentOption: "partial-upfront", LookbackPeriod: fmt.Sprintf("%dd", days),
			}}, f.params)
		})
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
		{"absent", &fakeSPRateSource{err: fmt.Errorf("wrapped: %w", recommendations.ErrNoSPCommitmentRate)}, nil},
		{"hard", &fakeSPRateSource{err: hard}, hard},
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
