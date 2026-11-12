package ladder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

const testSub = "sub-abc"

const (
	planID  = "/providers/Microsoft.BillingBenefits/savingsPlanOrders/order-1/savingsPlans/plan-1"
	planID2 = "/providers/Microsoft.BillingBenefits/savingsPlanOrders/order-1/savingsPlans/plan-2"
)

type fakeLister struct {
	err   error
	out   []common.Commitment
	calls int
}

func (f *fakeLister) GetExistingCommitments(context.Context) ([]common.Commitment, error) {
	f.calls++
	return f.out, f.err
}

type fakeSPUtil struct {
	err      error
	out      map[string]float64
	orderIDs []string
	calls    int
}

func (f *fakeSPUtil) GetSavingsPlanUtilization(_ context.Context, orderIDs []string, _, _ time.Time) (map[string]float64, error) {
	f.calls++
	f.orderIDs = orderIDs
	return f.out, f.err
}

type fakeRIUtil struct {
	err   error
	out   []common.RIUtilization
	calls int
}

func (f *fakeRIUtil) GetRIUtilization(context.Context, int) ([]common.RIUtilization, error) {
	f.calls++
	return f.out, f.err
}

type fakes struct {
	ris, sps *fakeLister
	spUtil   *fakeSPUtil
	riUtil   *fakeRIUtil
}

func newTestLadder(t *testing.T) (*AzureLadder, *fakes) {
	t.Helper()
	f := &fakes{ris: &fakeLister{}, sps: &fakeLister{}, spUtil: &fakeSPUtil{}, riUtil: &fakeRIUtil{}}
	a, err := NewAzureLadder(testSub, f.ris, f.sps, f.spUtil, f.riUtil)
	require.NoError(t, err)
	return a, f
}

func testScope() ladder.Scope {
	return ladder.Scope{Provider: common.ProviderAzure, AccountID: testSub}
}

func (f *fakes) totalCalls() int {
	return f.ris.calls + f.sps.calls + f.spUtil.calls + f.riUtil.calls
}

func TestAzureLadder_Provider(t *testing.T) {
	t.Parallel()
	a, _ := newTestLadder(t)
	assert.Equal(t, common.ProviderAzure, a.Provider())
}

func TestAzureLadder_SupportedLayersPassEngineValidation(t *testing.T) {
	t.Parallel()
	a, _ := newTestLadder(t)
	layers := a.SupportedLayers()
	require.Len(t, layers, 2)

	states, err := a.GetLayerStates(context.Background(), testScope())
	require.NoError(t, err)
	_, err = ladder.Allocate(&ladder.AllocationInput{
		Config: ladder.LadderConfig{
			Scope:                         testScope(),
			TargetCoveragePct:             100,
			BufferFraction:                0.1,
			BaselinePercentile:            5,
			LookbackDays:                  30,
			Mode:                          ladder.ModeEmailApproval,
			Cadence:                       ladder.CadenceWeekly,
			Ramp:                          ladder.RampSchedule{Steps: []ladder.RampStep{{AfterDays: 0, Fraction: 1.0}}},
			MaxActionsPerRun:              10,
			BufferUtilizationThresholdPct: ladder.DefaultBufferUtilizationThresholdPct,
		},
		Layers:          layers,
		LayerStates:     states,
		Baseline:        ladder.UsageBaseline{},
		Now:             time.Now(),
		InFlight:        []ladder.InFlightTranche{},
		CommitmentRates: map[ladder.LayerType]float64{ladder.LayerAzureReservation: 1, ladder.LayerAzureSavingsPlan: 1},
	})
	require.NoError(t, err)
}

func TestAzureLadder_GetLayerStatesEmptyIsExplicitZero(t *testing.T) {
	t.Parallel()
	a, f := newTestLadder(t)
	states, err := a.GetLayerStates(context.Background(), testScope())
	require.NoError(t, err)
	require.Len(t, states, 2)
	for layer, s := range states {
		require.NotNil(t, s.ExistingUSDPerHour, layer)
		require.NotNil(t, s.ExpiringUSDPerHour, layer)
		assert.Zero(t, *s.ExistingUSDPerHour)
		assert.Zero(t, *s.ExpiringUSDPerHour)
		assert.Nil(t, s.UtilizationPct)
	}
	assert.Zero(t, f.spUtil.calls+f.riUtil.calls, "empty layers must not query utilization")
}

func TestAzureLadder_GetLayerStatesUtilization(t *testing.T) {
	t.Parallel()
	a, f := newTestLadder(t)
	soon, later := time.Now().Add(24*time.Hour), time.Now().Add(400*24*time.Hour)
	f.sps.out = []common.Commitment{
		{CommitmentID: planID, State: common.CommitmentStateActive, Cost: 2, CostKnown: true, CostCurrency: "USD", EndDate: soon},
		{CommitmentID: planID2, State: common.CommitmentStateActive, Cost: 6, CostKnown: true, CostCurrency: "USD", EndDate: later},
		{CommitmentID: "/x/savingsPlanOrders/order-9/savingsPlans/p", State: common.CommitmentStateExpired, Cost: 100, CostKnown: true, CostCurrency: "USD"},
	}
	f.ris.out = []common.Commitment{
		{CommitmentID: "/providers/Microsoft.Capacity/reservationOrders/o/reservations/ri-1", State: common.CommitmentStateActive, Cost: 1, CostKnown: true, CostCurrency: "USD", EndDate: soon},
		{CommitmentID: "ri-2", State: common.CommitmentStateActive, Cost: 4, CostKnown: true, CostCurrency: "USD", EndDate: later},
	}
	f.spUtil.out = map[string]float64{"plan-1": 80, "plan-2": 40}
	f.riUtil.out = []common.RIUtilization{
		{ReservedInstanceID: "RI-1", UtilizationPercent: 90, PurchasedHours: 100, TotalActualHours: 90},
		{ReservedInstanceID: "ri-2", UtilizationPercent: 50, PurchasedHours: 300, TotalActualHours: 150},
		{ReservedInstanceID: "other-sku-ri", UtilizationPercent: 0, PurchasedHours: 1000},
	}

	states, err := a.GetLayerStates(context.Background(), testScope())
	require.NoError(t, err)

	sp := states[ladder.LayerAzureSavingsPlan]
	assert.InDelta(t, 8.0, *sp.ExistingUSDPerHour, 1e-9)
	assert.InDelta(t, 2.0, *sp.ExpiringUSDPerHour, 1e-9)
	require.NotNil(t, sp.UtilizationPct)
	assert.InDelta(t, (80*2+40*6)/8.0, *sp.UtilizationPct, 1e-9, "weighted by cost")
	assert.Equal(t, []string{"order-1"}, f.spUtil.orderIDs)

	ri := states[ladder.LayerAzureReservation]
	assert.InDelta(t, 5.0, *ri.ExistingUSDPerHour, 1e-9)
	assert.InDelta(t, 1.0, *ri.ExpiringUSDPerHour, 1e-9, "reservation expiring within the horizon")
	require.NotNil(t, ri.UtilizationPct)
	assert.InDelta(t, (90*100+50*300)/400.0, *ri.UtilizationPct, 1e-9, "hours-weighted, foreign reservations excluded")
}

func TestAzureLadder_PaymentPendingCountsAsExisting(t *testing.T) {
	t.Parallel()
	for _, state := range []common.CommitmentState{common.CommitmentStatePaymentPending, common.CommitmentStateQueued, common.CommitmentStateFailed} {
		a, f := newTestLadder(t)
		f.sps.out = []common.Commitment{{CommitmentID: planID, State: state, Cost: 3, CostKnown: true, CostCurrency: "USD"}}
		states, err := a.GetLayerStates(context.Background(), testScope())
		require.NoError(t, err)
		want := 0.0
		if state == common.CommitmentStatePaymentPending {
			want = 3
		}
		assert.InDelta(t, want, *states[ladder.LayerAzureSavingsPlan].ExistingUSDPerHour, 1e-9, string(state))
	}
}

func TestAzureLadder_UnmatchedUtilizationIDsFailLoud(t *testing.T) {
	t.Parallel()
	a, f := newTestLadder(t)
	f.sps.out = []common.Commitment{{CommitmentID: planID, State: common.CommitmentStateActive, Cost: 3, CostKnown: true, CostCurrency: "USD"}}
	f.spUtil.out = map[string]float64{"unrelated": 50}
	_, err := a.GetLayerStates(context.Background(), testScope())
	require.ErrorContains(t, err, "none match")
}

func TestAzureLadder_GetLayerStatesFailsLoud(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	activeSP := []common.Commitment{{CommitmentID: planID, State: common.CommitmentStateActive, CostKnown: true, CostCurrency: "USD"}}
	activeRI := []common.Commitment{{CommitmentID: "ri", State: common.CommitmentStateActive, CostKnown: true, CostCurrency: "USD"}}
	tests := []struct {
		setup func(f *fakes)
		name  string
		want  string
	}{
		{name: "reservation listing", want: "boom", setup: func(f *fakes) { f.ris.err = boom }},
		{name: "savings plan listing", want: "boom", setup: func(f *fakes) { f.sps.err = boom }},
		{name: "savings plan utilization", want: "boom", setup: func(f *fakes) { f.sps.out = activeSP; f.spUtil.err = boom }},
		{name: "reservation utilization", want: "boom", setup: func(f *fakes) { f.ris.out = activeRI; f.riUtil.err = boom }},
		{name: "reservation without a price", want: "no known price", setup: func(f *fakes) {
			f.ris.out = []common.Commitment{{CommitmentID: "ri", State: common.CommitmentStateActive}}
		}},
		{name: "savings plan in another currency", want: "EUR", setup: func(f *fakes) {
			f.sps.out = []common.Commitment{{CommitmentID: planID, State: common.CommitmentStateActive, CostKnown: true, CostCurrency: "EUR"}}
		}},
		{name: "savings plan id without order", want: "savingsPlanOrders", setup: func(f *fakes) {
			f.sps.out = []common.Commitment{{CommitmentID: "plan", State: common.CommitmentStateActive, CostKnown: true, CostCurrency: "USD"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, f := newTestLadder(t)
			tt.setup(f)
			_, err := a.GetLayerStates(context.Background(), testScope())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestAzureLadder_ListCommitmentsMergesAndFailsLoud(t *testing.T) {
	t.Parallel()
	a, f := newTestLadder(t)
	f.ris.out = []common.Commitment{{CommitmentID: "ri"}}
	f.sps.out = []common.Commitment{{CommitmentID: "sp"}}
	got, err := a.ListCommitments(context.Background(), testScope())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "ri", got[0].CommitmentID)
	assert.Equal(t, "sp", got[1].CommitmentID)

	f.sps.err = errors.New("boom")
	_, err = a.ListCommitments(context.Background(), testScope())
	require.ErrorContains(t, err, "boom")

	_, err = a.ListCommitments(context.Background(), ladder.Scope{Provider: common.ProviderAzure, AccountID: "other"})
	require.Error(t, err)
}

func TestAzureLadder_GetUsageBaselineIsBlocked(t *testing.T) {
	t.Parallel()
	a, f := newTestLadder(t)
	_, err := a.GetUsageBaseline(context.Background(), testScope(), 30, 5)
	assert.ErrorIs(t, err, ErrBaselineBasisUnresolved)
	assert.Zero(t, f.totalCalls())
}

func TestNewAzureLadderRejectsNil(t *testing.T) {
	t.Parallel()
	_, err := NewAzureLadder("", &fakeLister{}, &fakeLister{}, &fakeSPUtil{}, &fakeRIUtil{})
	require.Error(t, err)
	_, err = NewAzureLadder(testSub, nil, &fakeLister{}, &fakeSPUtil{}, &fakeRIUtil{})
	require.Error(t, err)
}
