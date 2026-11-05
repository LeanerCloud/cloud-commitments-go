package ladder

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
)

// Pins ladder.ObservedCommitmentRateCapability's signature without naming it:
// providers/aws builds standalone against the released pkg, which predates the
// interface. Switch to the named assertion after the pkg pin bump.
var _ interface {
	GetObservedCommitmentRates(context.Context, ladder.Scope) (map[ladder.LayerType]float64, error)
} = (*AWSLadder)(nil)

type spObsCall struct {
	plan            cetypes.SupportedSavingsPlansType
	account, region string
	days            int
}

type fakeObservedSource struct {
	spSummaries map[cetypes.SupportedSavingsPlansType]recommendations.SPUtilizationSummary
	spErr       error
	fees        []recommendations.RIFeeUtilization
	feeErr      error
	spCalls     []spObsCall
	feeCalls    []spObsCall
}

func (f *fakeObservedSource) GetSPUtilizationForAccount(_ context.Context, p cetypes.SupportedSavingsPlansType, acct, region string, days int) (recommendations.SPUtilizationSummary, error) {
	f.spCalls = append(f.spCalls, spObsCall{p, acct, region, days})
	return f.spSummaries[p], f.spErr
}

func (f *fakeObservedSource) GetRIFeeUtilization(_ context.Context, acct, region string, days int) ([]recommendations.RIFeeUtilization, error) {
	f.feeCalls = append(f.feeCalls, spObsCall{"", acct, region, days})
	return f.fees, f.feeErr
}

func spSummary(used float64) recommendations.SPUtilizationSummary {
	return recommendations.SPUtilizationSummary{UsedCommitmentUSDPerHour: aws.Float64(used), OnDemandCostEquivalentUSDPerHour: aws.Float64(10)}
}

func obsLadder(src *fakeObservedSource, ris ...ec2svc.ConvertibleRI) *AWSLadder {
	a := &AWSLadder{
		cfg: Config{Region: "eu-west-1", AccountID: testScope().AccountID, LookbackDays: 14},
		ris: &fakeRILister{ris: ris},
	}
	if src != nil {
		a.observed = src
	}
	return a
}

func cri(id string) ec2svc.ConvertibleRI { return ec2svc.ConvertibleRI{ReservedInstanceID: id} }

func TestGetObservedCommitmentRates_ValidatesBeforeSource(t *testing.T) {
	t.Parallel()
	for name, scope := range map[string]ladder.Scope{
		"provider": {Provider: common.ProviderAzure, AccountID: testScope().AccountID},
		"account":  {Provider: common.ProviderAWS, AccountID: "wrong"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeObservedSource{}
			got, err := obsLadder(f).GetObservedCommitmentRates(context.Background(), scope)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Empty(t, f.spCalls)
			assert.Empty(t, f.feeCalls)
		})
	}
}

func TestGetObservedCommitmentRates_NilSource(t *testing.T) {
	t.Parallel()
	got, err := obsLadder(nil).GetObservedCommitmentRates(context.Background(), testScope())
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestGetObservedCommitmentRates_ScopesAndValues(t *testing.T) {
	t.Parallel()
	f := &fakeObservedSource{
		spSummaries: map[cetypes.SupportedSavingsPlansType]recommendations.SPUtilizationSummary{
			cetypes.SupportedSavingsPlansTypeEc2InstanceSp: spSummary(7),
			cetypes.SupportedSavingsPlansTypeComputeSp:     spSummary(8),
		},
		fees: []recommendations.RIFeeUtilization{
			{ReservedInstanceID: "ri-1", TotalAmortizedFee: 100, OnDemandCostOfRIHoursUsed: 62.5, PurchasedHours: 200, TotalActualHours: 100},
			{ReservedInstanceID: "std-1", TotalAmortizedFee: 1, OnDemandCostOfRIHoursUsed: 100, PurchasedHours: 0, TotalActualHours: 1},
		},
	}
	got, err := obsLadder(f, cri("ri-1")).GetObservedCommitmentRates(context.Background(), testScope())
	require.NoError(t, err)
	assert.InDelta(t, 0.7, got[ladder.LayerEC2InstanceSP], 1e-9)
	assert.InDelta(t, 0.8, got[ladder.LayerComputeSP], 1e-9)
	// Decision (a): the used-hours fee is 100*0.5=50, over OD 62.5 = 0.8; the
	// literal fee/OD would be 1.6 and be dropped.
	assert.InDelta(t, 0.8, got[ladder.LayerConvertibleRI], 1e-9)
	for _, v := range got {
		assert.Greater(t, v, 0.0)
		assert.LessOrEqual(t, v, 1.0)
	}
	acct := testScope().AccountID
	assert.ElementsMatch(t, []spObsCall{
		{cetypes.SupportedSavingsPlansTypeEc2InstanceSp, acct, "eu-west-1", 14},
		{cetypes.SupportedSavingsPlansTypeComputeSp, acct, "", 14},
	}, f.spCalls)
	assert.Equal(t, []spObsCall{{"", acct, "eu-west-1", 14}}, f.feeCalls)
}

func TestGetObservedCommitmentRates_RatioOfSums(t *testing.T) {
	t.Parallel()
	f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{
		// used fee 10 (ratio 0.5 on OD 20) and used fee 90 (ratio 0.9 on OD 100)
		{ReservedInstanceID: "a", TotalAmortizedFee: 20, OnDemandCostOfRIHoursUsed: 20, PurchasedHours: 100, TotalActualHours: 50},
		{ReservedInstanceID: "b", TotalAmortizedFee: 100, OnDemandCostOfRIHoursUsed: 100, PurchasedHours: 100, TotalActualHours: 90},
	}}
	got, err := obsLadder(f, cri("a"), cri("b")).GetObservedCommitmentRates(context.Background(), testScope())
	require.NoError(t, err)
	assert.InDelta(t, 100.0/120.0, got[ladder.LayerConvertibleRI], 1e-9, "ratio of sums, not mean of ratios (0.7)")
}

func TestGetObservedCommitmentRates_AbsentKeys(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sp   recommendations.SPUtilizationSummary
		fees []recommendations.RIFeeUtilization
	}{
		"nothing observed": {},
		"sp ratio above one": {
			sp: spSummary(11),
		},
		"ri above one": {
			fees: []recommendations.RIFeeUtilization{{ReservedInstanceID: "r", TotalAmortizedFee: 10, OnDemandCostOfRIHoursUsed: 5, PurchasedHours: 10, TotalActualHours: 10}},
		},
		"ri zero fee": {
			fees: []recommendations.RIFeeUtilization{{ReservedInstanceID: "r", OnDemandCostOfRIHoursUsed: 5, PurchasedHours: 10, TotalActualHours: 10}},
		},
		"ri no match": {
			fees: []recommendations.RIFeeUtilization{{ReservedInstanceID: "other", TotalAmortizedFee: 1, OnDemandCostOfRIHoursUsed: 5, PurchasedHours: 10, TotalActualHours: 10}},
		},
		"ri zero denominator": {
			fees: []recommendations.RIFeeUtilization{{ReservedInstanceID: "r", TotalAmortizedFee: 1, PurchasedHours: 10, TotalActualHours: 10}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeObservedSource{
				spSummaries: map[cetypes.SupportedSavingsPlansType]recommendations.SPUtilizationSummary{
					cetypes.SupportedSavingsPlansTypeEc2InstanceSp: tc.sp, cetypes.SupportedSavingsPlansTypeComputeSp: tc.sp,
				},
				fees: tc.fees,
			}
			got, err := obsLadder(f, cri("r")).GetObservedCommitmentRates(context.Background(), testScope())
			require.NoError(t, err)
			for _, l := range []ladder.LayerType{ladder.LayerEC2InstanceSP, ladder.LayerComputeSP, ladder.LayerConvertibleRI} {
				_, ok := got[l]
				assert.False(t, ok, l)
			}
		})
	}
}

func TestGetObservedCommitmentRates_Errors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	for name, f := range map[string]*fakeObservedSource{
		"sp":  {spErr: boom},
		"fee": {feeErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := obsLadder(f, cri("r")).GetObservedCommitmentRates(context.Background(), testScope())
			require.ErrorIs(t, err, boom)
			assert.Nil(t, got)
		})
	}
	t.Run("ri list", func(t *testing.T) {
		a := obsLadder(&fakeObservedSource{})
		a.ris = &fakeRILister{err: boom}
		got, err := a.GetObservedCommitmentRates(context.Background(), testScope())
		require.ErrorIs(t, err, boom)
		assert.Nil(t, got)
	})
}

func TestGetObservedCommitmentRates_PurchasedHoursZero(t *testing.T) {
	// Not parallel: captures the global logger.
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	good := recommendations.RIFeeUtilization{ReservedInstanceID: "good", TotalAmortizedFee: 10, OnDemandCostOfRIHoursUsed: 20, PurchasedHours: 10, TotalActualHours: 10}

	t.Run("zero everything is skipped", func(t *testing.T) {
		f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{good, {ReservedInstanceID: "empty"}}}
		got, err := obsLadder(f, cri("good"), cri("empty")).GetObservedCommitmentRates(context.Background(), testScope())
		require.NoError(t, err)
		assert.InDelta(t, 0.5, got[ladder.LayerConvertibleRI], 1e-9)
	})
	t.Run("fee with no hours drops the key with a warning", func(t *testing.T) {
		buf.Reset()
		f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{good, {ReservedInstanceID: "bad", TotalAmortizedFee: 3}}}
		got, err := obsLadder(f, cri("good"), cri("bad")).GetObservedCommitmentRates(context.Background(), testScope())
		require.NoError(t, err)
		_, ok := got[ladder.LayerConvertibleRI]
		assert.False(t, ok)
		assert.Contains(t, buf.String(), "WARNING")
	})
	t.Run("non-convertible bad row is ignored", func(t *testing.T) {
		f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{good, {ReservedInstanceID: "std", TotalAmortizedFee: 3}}}
		got, err := obsLadder(f, cri("good")).GetObservedCommitmentRates(context.Background(), testScope())
		require.NoError(t, err)
		assert.InDelta(t, 0.5, got[ladder.LayerConvertibleRI], 1e-9)
	})
}

func TestNewFromConfig_WiresObservedSource(t *testing.T) {
	t.Parallel()
	a, err := NewFromConfig(aws.Config{}, Config{Region: "eu-west-1", AccountID: testScope().AccountID})
	require.NoError(t, err)
	require.NotNil(t, a.observed)
}

func TestGetObservedCommitmentRates_SPWarnsOnlyOnOutOfRange(t *testing.T) {
	// Not parallel: captures the global logger.
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	run := func(sp recommendations.SPUtilizationSummary) {
		buf.Reset()
		f := &fakeObservedSource{spSummaries: map[cetypes.SupportedSavingsPlansType]recommendations.SPUtilizationSummary{
			cetypes.SupportedSavingsPlansTypeEc2InstanceSp: sp,
		}}
		_, err := obsLadder(f).GetObservedCommitmentRates(context.Background(), testScope())
		require.NoError(t, err)
	}
	run(recommendations.SPUtilizationSummary{})
	assert.Empty(t, buf.String(), "no observation is silent")
	run(spSummary(11))
	assert.Contains(t, buf.String(), "WARNING")
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

func TestGetObservedCommitmentRates_NoRIsSkipsFeeCall(t *testing.T) {
	t.Parallel()
	f := &fakeObservedSource{}
	got, err := obsLadder(f).GetObservedCommitmentRates(context.Background(), testScope())
	require.NoError(t, err)
	assert.Empty(t, f.feeCalls, "a billed CE call is wasted when there is nothing to join")
	_, ok := got[ladder.LayerConvertibleRI]
	assert.False(t, ok)
}

func TestGetObservedCommitmentRates_MalformedRIHoursWarnAndDrop(t *testing.T) {
	// Not parallel: captures the global logger.
	good := recommendations.RIFeeUtilization{ReservedInstanceID: "good", TotalAmortizedFee: 10, OnDemandCostOfRIHoursUsed: 20, PurchasedHours: 10, TotalActualHours: 10}
	for name, bad := range map[string]recommendations.RIFeeUtilization{
		"actual hours with no purchased hours": {ReservedInstanceID: "bad", TotalActualHours: 5},
		"actual above purchased":               {ReservedInstanceID: "bad", TotalAmortizedFee: 1, OnDemandCostOfRIHoursUsed: 2, PurchasedHours: 10, TotalActualHours: 11},
	} {
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)
			f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{good, bad}}
			got, err := obsLadder(f, cri("good"), cri("bad")).GetObservedCommitmentRates(context.Background(), testScope())
			require.NoError(t, err)
			_, ok := got[ladder.LayerConvertibleRI]
			assert.False(t, ok)
			assert.Contains(t, buf.String(), "bad")
			assert.NotContains(t, buf.String(), "outside (0, 1]", "the specific warning, not the generic range one")
		})
	}
}

// CE hour strings summed across rows can put a fully used RI's actual hours a
// float hair above its purchased hours; that is not malformed data.
func TestGetObservedCommitmentRates_FullyUsedRIWithinFloatTolerance(t *testing.T) {
	t.Parallel()
	f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{
		{ReservedInstanceID: "r", TotalAmortizedFee: 10, OnDemandCostOfRIHoursUsed: 20, PurchasedHours: 720, TotalActualHours: 720 + 1e-9},
	}}
	got, err := obsLadder(f, cri("r")).GetObservedCommitmentRates(context.Background(), testScope())
	require.NoError(t, err)
	require.Contains(t, got, ladder.LayerConvertibleRI)
	assert.InDelta(t, 0.5, got[ladder.LayerConvertibleRI], 1e-9, "actual is clamped to purchased")
}

func TestGetObservedCommitmentRates_ZeroDenominatorIsSilent(t *testing.T) {
	// Not parallel: captures the global logger.
	buf := captureLog(t)
	f := &fakeObservedSource{fees: []recommendations.RIFeeUtilization{{ReservedInstanceID: "r", TotalAmortizedFee: 1, PurchasedHours: 10, TotalActualHours: 10}}}
	got, err := obsLadder(f, cri("r")).GetObservedCommitmentRates(context.Background(), testScope())
	require.NoError(t, err)
	_, ok := got[ladder.LayerConvertibleRI]
	assert.False(t, ok)
	assert.Empty(t, buf.String(), "|| onDemand == 0 must return before the ratio is computed")
}

func TestGetObservedCommitmentRates_PartialMap(t *testing.T) {
	t.Parallel()
	f := &fakeObservedSource{
		spSummaries: map[cetypes.SupportedSavingsPlansType]recommendations.SPUtilizationSummary{
			cetypes.SupportedSavingsPlansTypeEc2InstanceSp: spSummary(7),
		},
		fees: []recommendations.RIFeeUtilization{{ReservedInstanceID: "r", TotalAmortizedFee: 10, OnDemandCostOfRIHoursUsed: 20, PurchasedHours: 10, TotalActualHours: 10}},
	}
	got, err := obsLadder(f, cri("r")).GetObservedCommitmentRates(context.Background(), testScope())
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.InDelta(t, 0.7, got[ladder.LayerEC2InstanceSP], 1e-9)
	assert.InDelta(t, 0.5, got[ladder.LayerConvertibleRI], 1e-9)
	_, ok := got[ladder.LayerComputeSP]
	assert.False(t, ok)
}
