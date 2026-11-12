package ladder

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
)

const (
	resID    = "/providers/Microsoft.Capacity/reservationOrders/order-1/reservations/res-1"
	billing  = "/subscriptions/" + testSub
	sessionA = "session-a"
)

type fakeExchange struct {
	list        []compute.ExchangeableReservation
	preview     compute.ExchangePreview
	calcErr     error
	execErr     error
	listCalls   int
	calcCalls   int
	execCalls   int
	gotSources  []compute.ExchangeableReservation
	gotTargets  []compute.ExchangeTarget
	gotSession  string
	execStarted chan struct{}
	execRelease chan struct{}
}

func (f *fakeExchange) ListExchangeableReservations(context.Context) ([]compute.ExchangeableReservation, error) {
	f.listCalls++
	return f.list, nil
}

func (f *fakeExchange) CalculateExchange(_ context.Context, s []compute.ExchangeableReservation, tg []compute.ExchangeTarget) (*compute.ExchangePreview, []compute.CompatibleOffering, error) {
	f.calcCalls++
	f.gotSources, f.gotTargets = s, tg
	p := f.preview
	return &p, nil, f.calcErr
}

func (f *fakeExchange) ExecuteExchange(_ context.Context, id string) (*compute.ExchangeResult, error) {
	f.execCalls++
	f.gotSession = id
	if f.execRelease != nil {
		select {
		case f.execStarted <- struct{}{}:
			<-f.execRelease
		case <-time.After(time.Second):
		}
	}
	return &compute.ExchangeResult{SessionID: id}, f.execErr
}

func (f *fakeExchange) calls() int { return f.listCalls + f.calcCalls + f.execCalls }

func payable(v float64) compute.ExchangePreview {
	return compute.ExchangePreview{SessionID: sessionA, NetPayable: &v, NetPayableCurrency: "USD"}
}

func lowRes() compute.ExchangeableReservation {
	return compute.ExchangeableReservation{ReservationID: resID, ReservationOrderID: "order-1", BillingScopeID: billing, SKU: "Standard_D2s_v5", Region: "eastus", Term: "P1Y", Quantity: 10}
}

func capPtr(v float64) *float64 { return &v }

func goodCfg() ladder.BufferReshapeConfig {
	return ladder.BufferReshapeConfig{MaxPaymentPerExchangeUSD: capPtr(100), MaxPaymentDailyUSD: capPtr(150), UtilizationThresholdPct: 80, LookbackDays: 30}
}

func newReshapeLadder(t *testing.T, enabled bool, ex *fakeExchange, utilPct float64) (*AzureLadder, *fakes) {
	t.Helper()
	f := &fakes{ris: &fakeLister{}, sps: &fakeLister{}, spUtil: &fakeSPUtil{}, riUtil: &fakeRIUtil{out: []common.RIUtilization{{ReservedInstanceID: resID, UtilizationPercent: utilPct}}}}
	a, err := NewAzureLadder(testSub, f.ris, f.sps, f.spUtil, f.riUtil, WithPurchase(&fakePurchaser{t: t}, &fakePurchaser{t: t}, enabled), WithExchange(ex))
	require.NoError(t, err)
	return a, f
}

func TestAzureLadder_ReshapeBuffer_SwitchOffMakesNoCalls(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(1)}
	off, f := newReshapeLadder(t, false, ex, 20)
	def, fd := newTestLadder(t)
	for name, a := range map[string]*AzureLadder{"wired but off": off, "default": def} {
		_, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
		assert.ErrorIs(t, err, ErrLadderExecutionDisabled, name)
	}
	assert.Zero(t, ex.calls()+f.totalCalls()+fd.totalCalls())
}

func TestAzureLadder_ReshapeBuffer_EnabledWithoutExchangeIsNotWired(t *testing.T) {
	t.Parallel()
	a, err := NewAzureLadder(testSub, &fakeLister{}, &fakeLister{}, &fakeSPUtil{}, &fakeRIUtil{}, WithPurchase(&fakePurchaser{t: t}, &fakePurchaser{t: t}, true))
	require.NoError(t, err)
	_, err = a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	assert.ErrorIs(t, err, ErrWriteNotWired)
}

func TestAzureLadder_ReshapeBuffer_RefusesNilOrBadCaps(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ladder.BufferReshapeConfig){
		"nil per-exchange":  func(c *ladder.BufferReshapeConfig) { c.MaxPaymentPerExchangeUSD = nil },
		"nil daily":         func(c *ladder.BufferReshapeConfig) { c.MaxPaymentDailyUSD = nil },
		"zero per-exchange": func(c *ladder.BufferReshapeConfig) { c.MaxPaymentPerExchangeUSD = capPtr(0) },
		"zero daily":        func(c *ladder.BufferReshapeConfig) { c.MaxPaymentDailyUSD = capPtr(0) },
		"negative":          func(c *ladder.BufferReshapeConfig) { c.MaxPaymentPerExchangeUSD = capPtr(-1) },
		"NaN daily":         func(c *ladder.BufferReshapeConfig) { c.MaxPaymentDailyUSD = capPtr(math.NaN()) },
		"Inf per-exchange":  func(c *ladder.BufferReshapeConfig) { c.MaxPaymentPerExchangeUSD = capPtr(math.Inf(1)) },
		"threshold zero":    func(c *ladder.BufferReshapeConfig) { c.UtilizationThresholdPct = 0 },
		"threshold over":    func(c *ladder.BufferReshapeConfig) { c.UtilizationThresholdPct = 101 },
		"threshold NaN":     func(c *ladder.BufferReshapeConfig) { c.UtilizationThresholdPct = math.NaN() },
		"lookback zero":     func(c *ladder.BufferReshapeConfig) { c.LookbackDays = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(1)}
			a, f := newReshapeLadder(t, true, ex, 20)
			cfg := goodCfg()
			mutate(&cfg)
			_, err := a.ReshapeBuffer(context.Background(), testScope(), cfg)
			require.Error(t, err)
			assert.Zero(t, ex.calls()+f.totalCalls())
		})
	}
}

func TestAzureLadder_ReshapeBuffer_RejectsForeignScope(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(1)}
	a, f := newReshapeLadder(t, true, ex, 20)
	for _, s := range []ladder.Scope{{Provider: common.ProviderAWS, AccountID: testSub}, {Provider: common.ProviderAzure, AccountID: "other"}} {
		_, err := a.ReshapeBuffer(context.Background(), s, goodCfg())
		require.Error(t, err)
	}
	assert.Zero(t, ex.calls()+f.totalCalls())
}

func TestAzureLadder_ReshapeBuffer_OverPerExchangeCapRefusedWithoutExecute(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(100.01)}
	a, _ := newReshapeLadder(t, true, ex, 20)
	sum, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.NoError(t, err)
	assert.Equal(t, 1, ex.calcCalls)
	assert.Zero(t, ex.execCalls)
	assert.Equal(t, ladder.ReshapeSummary{Analyzed: 1, Skipped: 1}, ladder.ReshapeSummary{Analyzed: sum.Analyzed, Reshaped: sum.Reshaped, Skipped: sum.Skipped})
	assert.Contains(t, sum.Details[0], "per-exchange cap")
}

func TestAzureLadder_ReshapeBuffer_OverDailyCapRefusedWithoutExecute(t *testing.T) {
	t.Parallel()
	second := lowRes()
	second.ReservationID = "/providers/Microsoft.Capacity/reservationOrders/order-2/reservations/res-2"
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes(), second}, preview: payable(90)}
	a, f := newReshapeLadder(t, true, ex, 20)
	f.riUtil.out = append(f.riUtil.out, common.RIUtilization{ReservedInstanceID: second.ReservationID, UtilizationPercent: 20})

	sum, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.NoError(t, err)
	assert.Equal(t, 1, ex.execCalls, "second exchange would push the day to $180 over the $150 cap")
	assert.Equal(t, 1, sum.Reshaped)
	assert.Equal(t, 1, sum.Skipped)
	assert.Contains(t, sum.Details[1], "daily cap")

	_, err = a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.NoError(t, err)
	assert.Equal(t, 1, ex.execCalls, "a later run the same day still sees the $90 already spent")
}

func TestAzureLadder_ReshapeBuffer_ExecutesWithinCaps(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(100)}
	a, _ := newReshapeLadder(t, true, ex, 25)
	sum, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Reshaped)
	assert.Equal(t, 1, ex.execCalls)
	assert.Equal(t, sessionA, ex.gotSession)
	require.Len(t, ex.gotSources, 1)
	assert.Equal(t, resID, ex.gotSources[0].ReservationID)
	require.Len(t, ex.gotTargets, 1)
	assert.Equal(t, compute.ExchangeTarget{SKU: "Standard_D2s_v5", Location: "eastus", Term: "P1Y", BillingScopeID: billing, Quantity: 3}, ex.gotTargets[0])
}

func TestAzureLadder_ReshapeBuffer_DryRunNeverExecutes(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(10)}
	a, _ := newReshapeLadder(t, true, ex, 25)
	cfg := goodCfg()
	cfg.DryRun = true
	sum, err := a.ReshapeBuffer(context.Background(), testScope(), cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, ex.calcCalls)
	assert.Zero(t, ex.execCalls)
	assert.Zero(t, sum.Reshaped)
}

func TestAzureLadder_ReshapeBuffer_SkipsUnsafeCandidates(t *testing.T) {
	t.Parallel()
	foreign := lowRes()
	foreign.BillingScopeID = "/subscriptions/other"
	unknownPay := compute.ExchangePreview{SessionID: sessionA}
	policy := payable(1)
	policy.PolicyErrors = []string{"blocked"}
	for name, c := range map[string]struct {
		src     compute.ExchangeableReservation
		pct     float64
		preview compute.ExchangePreview
	}{
		"foreign billing scope": {foreign, 20, payable(1)},
		"above threshold":       {lowRes(), 90, payable(1)},
		"payment unknown":       {lowRes(), 20, unknownPay},
		"policy errors":         {lowRes(), 20, policy},
	} {
		ex := &fakeExchange{list: []compute.ExchangeableReservation{c.src}, preview: c.preview}
		a, _ := newReshapeLadder(t, true, ex, c.pct)
		sum, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
		require.NoError(t, err, name)
		assert.Zero(t, ex.execCalls, name)
		assert.Equal(t, 1, sum.Skipped, name)
	}
}

func TestAzureLadder_ReshapeBuffer_ExecuteFailureStopsAndErrors(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(10), execErr: errors.New("boom")}
	a, _ := newReshapeLadder(t, true, ex, 25)
	_, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.ErrorContains(t, err, "boom")
	assert.Equal(t, 1, ex.execCalls)
}

func TestAzureLadder_ReshapeBuffer_ExecuteErrorCountsPaymentAndHaltsTheDay(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(40), execErr: errors.New("boom")}
	a, _ := newReshapeLadder(t, true, ex, 25)
	_, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.Error(t, err)
	assert.InDelta(t, 40, a.dailySpend.spent(time.Now()), 1e-9)

	_, err = a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.ErrorIs(t, err, errExchangeHalted)
	assert.Equal(t, 1, ex.execCalls)
	assert.Equal(t, 1, ex.calcCalls)
}

func TestAzureLadder_ReshapeBuffer_HaltSurvivesUTCMidnight(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(40), execErr: errors.New("boom")}
	a, _ := newReshapeLadder(t, true, ex, 25)
	day := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return day }
	_, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.Error(t, err)

	day = day.Add(48 * time.Hour)
	_, err = a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
	require.ErrorIs(t, err, errExchangeHalted)
	assert.Equal(t, 1, ex.execCalls)
}

func TestAzureLadder_ReshapeBuffer_ConcurrentCallRechecksHaltUnderLock(t *testing.T) {
	t.Parallel()
	ex := &fakeExchange{list: []compute.ExchangeableReservation{lowRes()}, preview: payable(10), execErr: errors.New("boom"),
		execStarted: make(chan struct{}), execRelease: make(chan struct{})}
	a, _ := newReshapeLadder(t, true, ex, 25)
	errs := make(chan error, 2)
	run := func() {
		_, err := a.ReshapeBuffer(context.Background(), testScope(), goodCfg())
		errs <- err
	}
	go run()
	<-ex.execStarted
	go run()
	time.Sleep(50 * time.Millisecond)
	ex.execRelease <- struct{}{}

	// The two calls report in completion order, which is not fixed: the waiting
	// call can send its halt error before the executing call sends its own.
	var halted, failed int
	for range 2 {
		switch err := <-errs; {
		case errors.Is(err, errExchangeHalted):
			halted++
		case err != nil:
			failed++
		}
	}
	assert.Equal(t, 1, halted, "the waiting call must see the halt")
	assert.Equal(t, 1, failed, "the executing call must return its execute error")
	assert.Equal(t, 1, ex.execCalls)
}
