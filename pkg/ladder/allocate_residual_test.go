package ladder

import (
	"math/big"
	"math/rand"
	"strings"
	"testing"
)

// residualInput builds an AWS-shaped input (EC2 Instance SP base, Compute SP
// flex, Convertible RI buffer) with no buffer, no stable level, target pct and
// a low-water lw on the given basis. states defaults to all-zero layers.
func residualInput(basis BaselineBasis, lw, pct float64, states map[LayerType]LayerState, rates map[LayerType]float64, inFlight []InFlightTranche) *AllocationInput {
	layers := awsLayers()
	cfg := validConfigAWS()
	cfg.BufferFraction = 0
	cfg.TargetCoveragePct = pct
	if states == nil {
		states = zeroStates(layers)
	}
	return &AllocationInput{
		Config: cfg, Layers: layers, LayerStates: states,
		Baseline: UsageBaseline{LowWaterUSDPerHour: ptr(lw), Basis: basis},
		Now:      nowFixed(), DataSources: []string{"cost-explorer"},
		InFlight: inFlight, CommitmentRates: rates,
	}
}

func computeSPRate(r float64) map[LayerType]float64 {
	return map[LayerType]float64{LayerComputeSP: r, LayerEC2InstanceSP: r, LayerConvertibleRI: r}
}

func odTotal(res *AllocateResult) *big.Rat {
	sum := new(big.Rat)
	for _, a := range res.Allocations {
		sum.Add(sum, a.OnDemandEquivalentUSDPerHour)
	}
	return sum
}

func commitmentTotal(res *AllocateResult) *big.Rat {
	sum := new(big.Rat)
	for _, a := range res.Allocations {
		sum.Add(sum, a.GapUSDPerHour)
	}
	return sum
}

func requireSingleHold(t *testing.T, res *AllocateResult, want string) {
	t.Helper()
	if !res.IsNoOp() || len(res.Holds) != 1 || !strings.Contains(res.Holds[0].Rationale, want) {
		t.Fatalf("result = %+v, want exactly one Hold containing %q and no purchases", res, want)
	}
}

// TestAllocate_ResidualBaseline_Issue282Replay replays the go#282 numbers:
// target 100%, buffer 0, no in-flight, a $6/hr existing Compute SP and a
// $4/hr residual (uncovered) on-demand low water. On the total basis (the only
// behavior before the fix) the engine computes 4 - 6 = -2 and holds with
// "target already met" although $4/hr is uncovered. On the residual basis it
// sizes the $4/hr residual (commitment 4 * 0.7 = 2.8) and never more.
func TestAllocate_ResidualBaseline_Issue282Replay(t *testing.T) {
	t.Parallel()
	states := withExisting(zeroStates(awsLayers()), LayerComputeSP, 6)

	before := mustAllocate(t, residualInput(BaselineBasisTotal, 4, 100, states, computeSPRate(0.7), noInFlight()))
	requireSingleHold(t, before, "gap=$-2.0000/hr")

	res := mustAllocate(t, residualInput(BaselineBasisResidual, 4, 100, states, computeSPRate(0.7), noInFlight()))
	if len(res.Allocations) != 1 || res.Allocations[0].Layer != LayerComputeSP {
		t.Fatalf("allocations = %+v, want one compute-sp allocation", res.Allocations)
	}
	a := res.Allocations[0]
	ratEq(t, "od", a.OnDemandEquivalentUSDPerHour, 4, 1)
	approx(t, "commitment", a.GapUSDPerHour, 2.8)
	if !strings.Contains(a.Rationale, "baseline_basis=residual") || !strings.Contains(a.Rationale, "existing=$6.0000/hr") {
		t.Errorf("rationale %q must show existing and the residual basis", a.Rationale)
	}
}

// TestAllocate_ResidualBaseline_ZeroExistingMatchesTotal: with no existing
// commitment in any layer both bases agree (residual == total demand), at a
// full and a partial target: R=10 -> 10; R=10 at 80% -> 8.
func TestAllocate_ResidualBaseline_ZeroExistingMatchesTotal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pct    float64
		wantOD int64
	}{{100, 10}, {80, 8}} {
		for _, basis := range []BaselineBasis{"", BaselineBasisTotal, BaselineBasisResidual} {
			res := mustAllocate(t, residualInput(basis, 10, tc.pct, nil, unitRates(), noInFlight()))
			ratEq(t, string(basis)+" od", odTotal(res), tc.wantOD, 1)
		}
	}
}

// TestAllocate_ResidualBaseline_FullyCoveredHolds: existing >= demand leaves a
// zero residual: $6/hr Compute SP, R=0 -> target met. Around the $0.01
// minimum: R=0.005 holds (gap <= min), R=0.02 buys exactly 0.02.
func TestAllocate_ResidualBaseline_FullyCoveredHolds(t *testing.T) {
	t.Parallel()
	states := withExisting(zeroStates(awsLayers()), LayerComputeSP, 6)
	requireSingleHold(t, mustAllocate(t, residualInput(BaselineBasisResidual, 0, 100, states, unitRates(), noInFlight())), "target already met")
	requireSingleHold(t, mustAllocate(t, residualInput(BaselineBasisResidual, 0.005, 100, states, unitRates(), noInFlight())), "baseline_basis=residual")
	res := mustAllocate(t, residualInput(BaselineBasisResidual, 0.02, 100, states, unitRates(), noInFlight()))
	approx(t, "od", odTotal(res), 0.02)
}

// TestAllocate_ResidualBaseline_PartialTargetWithExistingHolds: at 80% the true
// gap is 0.8*(C+4) - C, which depends on the covered usage C the residual
// excludes, so nothing is bought (upper bound 0.8*4 = 3.2 is reported). When
// even that upper bound is below the minimum (R=0.01: 0.008) it is a plain
// target-met Hold.
func TestAllocate_ResidualBaseline_PartialTargetWithExistingHolds(t *testing.T) {
	t.Parallel()
	states := withExisting(zeroStates(awsLayers()), LayerComputeSP, 6)
	res := mustAllocate(t, residualInput(BaselineBasisResidual, 4, 80, states, unitRates(), noInFlight()))
	requireSingleHold(t, res, "residual baseline cannot size a partial target")
	if !strings.Contains(res.Holds[0].Rationale, "upper bound $3.2000/hr") {
		t.Errorf("rationale %q must report the 3.2 upper bound", res.Holds[0].Rationale)
	}
	requireSingleHold(t, mustAllocate(t, residualInput(BaselineBasisResidual, 0.01, 80, states, unitRates(), noInFlight())), "target already met")
}

// TestAllocate_ResidualBaseline_InFlightNetted: R=4 with $6/hr existing.
// 2.8 in flight at 0.7 is 4 OD -> hold; 1.4 at 0.7 is 2 OD -> buy OD 2
// (commitment 1.4).
func TestAllocate_ResidualBaseline_InFlightNetted(t *testing.T) {
	t.Parallel()
	states := withExisting(zeroStates(awsLayers()), LayerComputeSP, 6)
	full := []InFlightTranche{{Layer: LayerComputeSP, CommitmentUSDPerHour: 2.8, Rate: 0.7}}
	requireSingleHold(t, mustAllocate(t, residualInput(BaselineBasisResidual, 4, 100, states, computeSPRate(0.7), full)), "target already met")

	half := []InFlightTranche{{Layer: LayerComputeSP, CommitmentUSDPerHour: 1.4, Rate: 0.7}}
	res := mustAllocate(t, residualInput(BaselineBasisResidual, 4, 100, states, computeSPRate(0.7), half))
	approx(t, "od", odTotal(res), 2)
	approx(t, "commitment", commitmentTotal(res), 1.4)
}

// TestAllocate_ResidualBaseline_ExpiringNotAddedBack: $6/hr existing of which
// $2/hr expires. Total basis: 4 - (6-2) = 0 -> hold. Residual: 4, not 4+2:
// expiring coverage is still in effect in the observed window.
func TestAllocate_ResidualBaseline_ExpiringNotAddedBack(t *testing.T) {
	t.Parallel()
	states := withExisting(zeroStates(awsLayers()), LayerComputeSP, 6)
	st := states[LayerComputeSP]
	st.ExpiringUSDPerHour = ptr(2.0)
	states[LayerComputeSP] = st

	requireSingleHold(t, mustAllocate(t, residualInput(BaselineBasisTotal, 4, 100, states, unitRates(), noInFlight())), "target already met")
	ratEq(t, "residual od", odTotal(mustAllocate(t, residualInput(BaselineBasisResidual, 4, 100, states, unitRates(), noInFlight()))), 4, 1)
}

// TestAllocate_ResidualBaseline_BaseSplitIgnoresExistingBase: stable residual
// 5, existing base (EC2 Instance SP) 3, R=10, buffer 10%. The residual stable
// level already excludes the existing base, so base gets min(9, 5) = 5 (the
// total basis would give 5-3 = 2), flex 4, buffer 1; total OD stays R = 10.
func TestAllocate_ResidualBaseline_BaseSplitIgnoresExistingBase(t *testing.T) {
	t.Parallel()
	states := withExisting(zeroStates(awsLayers()), LayerEC2InstanceSP, 3)
	in := residualInput(BaselineBasisResidual, 10, 100, states, unitRates(), noInFlight())
	in.Config.BufferFraction = 0.10
	in.Baseline.StableUSDPerHour = ptr(5.0)
	res := mustAllocate(t, in)
	by := allocByLayer(res)
	approx(t, "base", by[LayerEC2InstanceSP].OnDemandEquivalentUSDPerHour, 5)
	approx(t, "flex", by[LayerComputeSP].OnDemandEquivalentUSDPerHour, 4)
	approx(t, "buffer", by[LayerConvertibleRI].OnDemandEquivalentUSDPerHour, 1)
	approx(t, "total", odTotal(res), 10)
}

func TestAllocate_UnknownBaselineBasisIsError(t *testing.T) {
	t.Parallel()
	if _, err := Allocate(residualInput("uncovered", 4, 100, nil, unitRates(), noInFlight())); err == nil || !strings.Contains(err.Error(), "baseline basis") {
		t.Fatalf("err = %v, want an unrecognized baseline basis error", err)
	}
}

// TestAllocate_ResidualBaseline_NeverExceedsResidual is a seeded property
// check over random existing/expiring/in-flight/rates/buffer/cap: on the
// residual basis the OD bought never exceeds R - in_flight_od (so never the
// true residual R), commitment never exceeds OD, and a partial target with
// existing commitment never buys.
func TestAllocate_ResidualBaseline_NeverExceedsResidual(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(282)) //nolint:gosec // deterministic test data
	for i := 0; i < 2000; i++ {
		in, residual, inFlightOD := randomResidualInput(rng)
		res := mustAllocate(t, in)
		od := odTotal(res)
		bound := new(big.Rat).Sub(residual, inFlightOD)
		if bound.Sign() < 0 {
			bound = new(big.Rat)
		}
		if od.Cmp(bound) > 0 || od.Cmp(residual) > 0 {
			t.Fatalf("case %d: bought OD %s > residual %s - in_flight %s", i, od.RatString(), residual.RatString(), inFlightOD.RatString())
		}
		if commitmentTotal(res).Cmp(od) > 0 {
			t.Fatalf("case %d: commitment %s > OD %s", i, commitmentTotal(res).RatString(), od.RatString())
		}
		if in.Config.TargetCoveragePct < 100 && hasExistingCommitment(in.Layers, in.LayerStates) && len(res.Allocations) > 0 {
			t.Fatalf("case %d: partial target with existing commitment bought %+v", i, res.Allocations)
		}
	}
}

func randomResidualInput(rng *rand.Rand) (in *AllocationInput, residual, inFlightOD *big.Rat) {
	layers := awsLayers()
	states := zeroStates(layers)
	for _, ls := range layers {
		if rng.Intn(2) == 0 {
			continue
		}
		e := float64(rng.Intn(2000)) / 100
		x := e * float64(rng.Intn(3)) / 2
		states[ls.Type] = LayerState{Layer: ls.Type, ExistingUSDPerHour: ptr(e), ExpiringUSDPerHour: ptr(x)}
	}
	rate := float64(rng.Intn(100)+1) / 100
	lw := float64(rng.Intn(2000)) / 100
	pct := 100.0
	if rng.Intn(3) == 0 {
		pct = float64(rng.Intn(99) + 1)
	}
	var inFlight []InFlightTranche
	inFlightOD = new(big.Rat)
	if rng.Intn(2) == 0 {
		c := float64(rng.Intn(500)) / 100
		inFlight = append(inFlight, InFlightTranche{Layer: LayerComputeSP, CommitmentUSDPerHour: c, Rate: rate})
		inFlightOD = new(big.Rat).Quo(new(big.Rat).SetFloat64(c), new(big.Rat).SetFloat64(rate))
	} else {
		inFlight = noInFlight()
	}
	in = residualInput(BaselineBasisResidual, lw, pct, states, computeSPRate(rate), inFlight)
	in.Config.BufferFraction = float64(rng.Intn(5)) / 10
	if rng.Intn(4) == 0 {
		in.Config.MaxHourlyCommitPerRun = ptr(float64(rng.Intn(500)+1) / 100)
	}
	return in, new(big.Rat).SetFloat64(lw), inFlightOD
}
