package ladder

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
)

// Acceptance replays for go#282 through the real AWS baseline, layer states and
// Allocate. Rates are passed in as the platform passes them (CommitmentRates).
func allocateResidualAccount(t *testing.T, series float64, sps []ActiveSP, ris []ec2svc.ConvertibleRI, rates map[ladder.LayerType]float64) *ladder.AllocateResult {
	t.Helper()
	ctx := context.Background()
	cov := &fakeCoverageSource{onDemandPoints: makeConstantPoints(7, series)}
	a := newTestLadder(t, &fakeRILister{ris: ris}, &fakeSPLister{sps: sps}, cov, &fakeUtilizationSource{})
	bl, err := a.GetUsageBaseline(ctx, testScope(), 7, 5.0)
	require.NoError(t, err)
	states, err := a.GetLayerStates(ctx, testScope())
	require.NoError(t, err)

	res, err := ladder.Allocate(&ladder.AllocationInput{
		Config: ladder.LadderConfig{Scope: testScope(), Mode: ladder.ModeEmailApproval, Cadence: ladder.CadenceWeekly,
			TargetCoveragePct: 100, BaselinePercentile: 5, LookbackDays: 7, MaxActionsPerRun: 10,
			BufferUtilizationThresholdPct: ladder.DefaultBufferUtilizationThresholdPct,
			Ramp:                          ladder.RampSchedule{Steps: []ladder.RampStep{{AfterDays: 0, Fraction: 1}}}},
		Layers: a.SupportedLayers(), LayerStates: states, CommitmentRates: rates,
		InFlight: []ladder.InFlightTranche{}, Baseline: bl,
		Now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), DataSources: []string{"constructed"},
	})
	require.NoError(t, err)
	return res
}

func ratFloat(t *testing.T, r *big.Rat) float64 {
	t.Helper()
	f, _ := r.Float64()
	return f
}

// A fully covered account has no uncovered on-demand usage, so the residual
// series is zero and nothing is bought.
func TestResidualAccount_FullyCoveredAccountHolds(t *testing.T) {
	t.Parallel()
	sps := []ActiveSP{makeSP("sp-compute-1", "Compute", 6.00, time.Now().AddDate(1, 0, 0))}
	res := allocateResidualAccount(t, 0, sps, nil, map[ladder.LayerType]float64{ladder.LayerComputeSP: 0.7})
	assert.True(t, res.IsNoOp())
	require.Len(t, res.Holds, 1)
	assert.Contains(t, res.Holds[0].Rationale, "target already met")
}

// EC2 Instance and Compute SPs already cover part of the account. The $4/hr
// uncovered residual is sized on the flex (Compute SP) layer at that layer's
// rate, and existing SP commitment is not netted against it again.
func TestResidualAccount_ResidualSizedAtFlexLayerRate(t *testing.T) {
	t.Parallel()
	end := time.Now().AddDate(1, 0, 0)
	sps := []ActiveSP{
		makeSP("sp-ec2-1", "EC2Instance", 3.00, end),
		makeSP("sp-compute-1", "Compute", 3.00, end),
	}
	rates := map[ladder.LayerType]float64{ladder.LayerEC2InstanceSP: 0.6, ladder.LayerComputeSP: 0.7}
	res := allocateResidualAccount(t, 4, sps, nil, rates)
	require.Empty(t, res.Holds)
	require.Len(t, res.Allocations, 1)
	a := res.Allocations[0]
	assert.Equal(t, ladder.LayerComputeSP, a.Layer)
	assert.InDelta(t, 4, ratFloat(t, a.OnDemandEquivalentUSDPerHour), 1e-9)
	assert.InDelta(t, 2.8, ratFloat(t, a.GapUSDPerHour), 1e-9)
}

// A convertible RI already covers $2/hr of the account. The $4/hr residual is
// the uncovered usage only, so the RI's $2/hr is not subtracted from it again.
func TestResidualAccount_ConvertibleRIExistingNotRenetted(t *testing.T) {
	t.Parallel()
	end := time.Now().AddDate(1, 0, 0)
	ris := []ec2svc.ConvertibleRI{makeRI("ri-1", "m5.large", 1, 2.00, end)}
	rates := map[ladder.LayerType]float64{ladder.LayerComputeSP: 0.7}
	res := allocateResidualAccount(t, 4, nil, ris, rates)
	require.NotEmpty(t, res.Allocations)

	od := new(big.Rat)
	for _, a := range res.Allocations {
		od.Add(od, a.OnDemandEquivalentUSDPerHour)
	}
	assert.InDelta(t, 4, ratFloat(t, od), 1e-9)
}
