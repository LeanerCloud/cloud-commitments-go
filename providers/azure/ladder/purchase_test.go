package ladder

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

type fakePurchaser struct {
	t      *testing.T
	forbid bool
	calls  int
	got    common.Recommendation
	gotOpt common.PurchaseOptions
	err    error
}

func (f *fakePurchaser) PurchaseCommitment(_ context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	if f.forbid {
		f.t.Errorf("purchase client called while the execution switch is off")
	}
	f.calls++
	f.got = rec
	f.gotOpt = opts
	return common.PurchaseResult{Success: f.err == nil, CommitmentID: "id"}, f.err
}

func newPurchaseLadder(t *testing.T, enabled bool, sp, ri *fakePurchaser) *AzureLadder {
	t.Helper()
	f := &fakes{ris: &fakeLister{}, sps: &fakeLister{}, spUtil: &fakeSPUtil{}, riUtil: &fakeRIUtil{}}
	a, err := NewAzureLadder(testSub, f.ris, f.sps, f.spUtil, f.riUtil, WithPurchase(sp, ri, enabled))
	require.NoError(t, err)
	return a
}

var testOpts = common.PurchaseOptions{IdempotencyToken: "tok-1", Source: "test"}

func spRec(amount float64) common.Recommendation {
	return common.Recommendation{
		Provider:       common.ProviderAzure,
		Service:        common.ServiceSavingsPlansAll,
		CommitmentType: common.CommitmentSavingsPlan,
		Details:        &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: amount},
	}
}

func riRec() common.Recommendation {
	return common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute, CommitmentType: common.CommitmentReservedInstance, ResourceType: "Standard_D2s_v5", Count: 1, Details: common.ComputeDetails{InstanceType: "Standard_D2s_v5"}}
}

func TestAzureLadder_PurchaseLayer_SwitchOffMakesNoCalls(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t, forbid: true}
	ri := &fakePurchaser{t: t, forbid: true}
	for name, a := range map[string]*AzureLadder{
		"wired but off": newPurchaseLadder(t, false, sp, ri),
		"default":       mustDefaultLadder(t),
	} {
		_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, spRec(1), testOpts)
		assert.ErrorIs(t, err, ErrLadderExecutionDisabled, name)
		_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, riRec(), testOpts)
		assert.ErrorIs(t, err, ErrLadderExecutionDisabled, name)
	}
	assert.Zero(t, sp.calls+ri.calls)
}

func mustDefaultLadder(t *testing.T) *AzureLadder {
	t.Helper()
	a, _ := newTestLadder(t)
	return a
}

func TestAzureLadder_PurchaseLayer_RoutesByLayer(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, ri)

	res, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, spRec(2), testOpts)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, 1, sp.calls)
	assert.Zero(t, ri.calls)
	assert.Equal(t, spRec(2), sp.got)
	assert.Equal(t, testOpts, sp.gotOpt)

	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, riRec(), testOpts)
	require.NoError(t, err)
	assert.Equal(t, 1, sp.calls)
	assert.Equal(t, 1, ri.calls)
	assert.Equal(t, riRec(), ri.got)
	assert.Equal(t, testOpts, ri.gotOpt)
}

func TestAzureLadder_PurchaseLayer_RefusesEmptyToken(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, ri)
	opts := common.PurchaseOptions{Source: "test"}
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, spRec(1), opts)
	require.Error(t, err)
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, riRec(), opts)
	require.Error(t, err)
	assert.Zero(t, sp.calls+ri.calls)
}

func TestAzureLadder_PurchaseLayer_RefusesEmptySource(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, ri)
	opts := common.PurchaseOptions{IdempotencyToken: "tok-1"}
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, spRec(1), opts)
	require.Error(t, err)
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, riRec(), opts)
	require.Error(t, err)
	assert.Zero(t, sp.calls+ri.calls)
}

func TestAzureLadder_PurchaseLayer_RefusesAWSOnlySavingsPlanServices(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, &fakePurchaser{t: t})
	for _, svc := range []common.ServiceType{common.ServiceSavingsPlansEC2Instance, common.ServiceSavingsPlansSageMaker, common.ServiceSavingsPlansDatabase} {
		rec := spRec(1)
		rec.Service = svc
		_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, rec, testOpts)
		assert.Error(t, err, svc)
	}
	assert.Zero(t, sp.calls)
}

func TestAzureLadder_PurchaseLayer_RefusesCommitmentTypeAndDetailsMismatch(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, ri)

	badType := spRec(1)
	badType.CommitmentType = common.CommitmentReservedInstance
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, badType, testOpts)
	assert.Error(t, err)
	badType = riRec()
	badType.CommitmentType = common.CommitmentSavingsPlan
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, badType, testOpts)
	assert.Error(t, err)

	badDetails := spRec(1)
	badDetails.Details = common.ComputeDetails{}
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, badDetails, testOpts)
	assert.Error(t, err)
	badDetails = riRec()
	badDetails.Details = &common.SavingsPlanDetails{}
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, badDetails, testOpts)
	assert.Error(t, err)
	assert.Zero(t, sp.calls+ri.calls)
}

func TestAzureLadder_WithPurchase_RejectsNilClientsAndOptions(t *testing.T) {
	t.Parallel()
	f := &fakes{ris: &fakeLister{}, sps: &fakeLister{}, spUtil: &fakeSPUtil{}, riUtil: &fakeRIUtil{}}
	var typedNil *fakePurchaser
	ok := &fakePurchaser{t: t}
	for name, opt := range map[string]Option{
		"untyped nil sp": WithPurchase(nil, ok, true),
		"typed nil sp":   WithPurchase(typedNil, ok, true),
		"typed nil ri":   WithPurchase(ok, typedNil, true),
		"nil option":     nil,
	} {
		_, err := NewAzureLadder(testSub, f.ris, f.sps, f.spUtil, f.riUtil, opt)
		assert.Error(t, err, name)
	}
}

func TestAzureLadder_PurchaseLayer_RejectsUnknownLayer(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, ri)
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerComputeSP, spRec(1), testOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a supported Azure ladder layer")
	assert.Zero(t, sp.calls+ri.calls)
}

func TestAzureLadder_PurchaseLayer_RejectsMismatch(t *testing.T) {
	t.Parallel()
	sp := &fakePurchaser{t: t}
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, sp, ri)
	aws := spRec(1)
	aws.Provider = common.ProviderAWS
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, aws, testOpts)
	assert.Error(t, err)
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, riRec(), testOpts)
	assert.Error(t, err)
	_, err = a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, spRec(1), testOpts)
	assert.Error(t, err)
	assert.Zero(t, sp.calls+ri.calls)
}

func TestAzureLadder_PurchaseLayer_WrapsClientError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	a := newPurchaseLadder(t, true, &fakePurchaser{t: t, err: boom}, &fakePurchaser{t: t})
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, spRec(1), testOpts)
	assert.ErrorIs(t, err, boom)
}

func TestAzureLadder_PurchaseLayer_Precision(t *testing.T) {
	t.Parallel()
	refused := map[string]float64{
		"zero":          0,
		"negative":      -1,
		"NaN":           math.NaN(),
		"positive Inf":  math.Inf(1),
		"negative Inf":  math.Inf(-1),
		"below minimum": 0.0009,
		"above maximum": 1000000.01,
	}
	for name, amount := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			t.Parallel()
			sp := &fakePurchaser{t: t}
			a := newPurchaseLadder(t, true, sp, &fakePurchaser{t: t})
			_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, spRec(amount), testOpts)
			require.Error(t, err)
			assert.Zero(t, sp.calls)
		})
	}

	floored := map[string]struct{ in, want float64 }{
		"fraction floors, not rounds": {1.123459999, 1.12345},
		"exact amount unchanged":      {2.5, 2.5},
		"minimum accepted":            {0.001, 0.001},
		"maximum accepted":            {1000000, 1000000},
	}
	for name, c := range floored {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sp := &fakePurchaser{t: t}
			a := newPurchaseLadder(t, true, sp, &fakePurchaser{t: t})
			orig := spRec(c.in)
			_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureSavingsPlan, orig, testOpts)
			require.NoError(t, err)
			assert.Equal(t, c.want, sp.got.Details.(*common.SavingsPlanDetails).HourlyCommitment)
			assert.Equal(t, c.in, orig.Details.(*common.SavingsPlanDetails).HourlyCommitment, "caller's details must not change")
		})
	}
}

func TestAzureLadder_PurchaseLayer_RefusesNilComputeDetailsPointer(t *testing.T) {
	t.Parallel()
	ri := &fakePurchaser{t: t}
	a := newPurchaseLadder(t, true, &fakePurchaser{t: t}, ri)
	rec := riRec()
	rec.Details = (*common.ComputeDetails)(nil)
	_, err := a.PurchaseLayer(context.Background(), ladder.LayerAzureReservation, rec, testOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil *common.ComputeDetails")
	assert.Zero(t, ri.calls)
}
