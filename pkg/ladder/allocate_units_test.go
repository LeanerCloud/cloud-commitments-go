package ladder

import (
	"math"
	"math/big"
	"math/rand"
	"strings"
	"testing"
)

// unitsInput builds a flex-only style input: low water lw, no stable level
// (so the whole core gap goes to flex), buffer fraction bf.
func unitsInput(lw, bf float64, rates map[LayerType]float64, inFlight []InFlightTranche) *AllocationInput {
	layers := awsLayers()
	cfg := validConfigAWS()
	cfg.BufferFraction = bf
	return &AllocationInput{
		Config: cfg, Layers: layers, LayerStates: zeroStates(layers),
		Baseline: UsageBaseline{LowWaterUSDPerHour: ptr(lw)},
		Now:      nowFixed(), DataSources: []string{"cost-explorer"},
		InFlight: inFlight, CommitmentRates: rates,
	}
}

func allocByLayer(res *AllocateResult) map[LayerType]Allocation {
	out := map[LayerType]Allocation{}
	for _, a := range res.Allocations {
		out[a.Layer] = a
	}
	return out
}

func mustAllocate(t *testing.T, in *AllocationInput) *AllocateResult {
	t.Helper()
	res, err := Allocate(in)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	return res
}

func approx(t *testing.T, label string, got *big.Rat, want float64) {
	t.Helper()
	f, _ := got.Float64()
	if math.Abs(f-want) > 1e-9 {
		t.Errorf("%s = %g, want %g", label, f, want)
	}
}

// TestAllocate_OverBuyReplay replays the go#349 over-buy with numbers: $10/hr
// of uncovered on-demand usage, no existing commitment, a 0.7 commitment rate
// (30% discount). Before the conversion Allocate returned GapUSDPerHour = 10
// (commitment dollars bought 1:1, 43% too much); a second run with 7 in flight
// netted 10 - 7 = 3 and bought again. Now the commitment is 7 and the
// in-flight 7 (at its stored rate 0.7) nets the gap to zero.
func TestAllocate_OverBuyReplay(t *testing.T) {
	t.Parallel()
	rates := map[LayerType]float64{LayerComputeSP: 0.7}
	res := mustAllocate(t, unitsInput(10, 0, rates, noInFlight()))
	if len(res.Allocations) != 1 {
		t.Fatalf("allocations = %+v, want 1", res.Allocations)
	}
	a := res.Allocations[0]
	approx(t, "commitment", a.GapUSDPerHour, 7)
	ratEq(t, "on-demand-equivalent", a.OnDemandEquivalentUSDPerHour, 10, 1)

	res2 := mustAllocate(t, unitsInput(10, 0, rates, []InFlightTranche{{Layer: LayerComputeSP, CommitmentUSDPerHour: 7, Rate: 0.7}}))
	if !res2.IsNoOp() || len(res2.Holds) != 1 || !strings.Contains(res2.Holds[0].Rationale, "target already met") {
		t.Fatalf("second run = %+v, want a target-met Hold (no re-buy)", res2)
	}
}

func TestAllocate_RateOneIsIdentity(t *testing.T) {
	t.Parallel()
	res := mustAllocate(t, unitsInput(10, 0, unitRates(), noInFlight()))
	a := res.Allocations[0]
	ratEq(t, "commitment", a.GapUSDPerHour, 10, 1)
	ratEq(t, "od", a.OnDemandEquivalentUSDPerHour, 10, 1)
}

func TestAllocate_InFlightNettedAtStoredRateNotCurrent(t *testing.T) {
	t.Parallel()
	// 3.5 commitment @ stored 0.5 = 7 OD; current rate 0.75; gap 10-7 = 3 OD.
	inflight := []InFlightTranche{{Layer: LayerComputeSP, CommitmentUSDPerHour: 3.5, Rate: 0.5}}
	res := mustAllocate(t, unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: 0.75}, inflight))
	a := res.Allocations[0]
	ratEq(t, "od", a.OnDemandEquivalentUSDPerHour, 3, 1)
	ratEq(t, "commitment", a.GapUSDPerHour, 9, 4)
}

func TestAllocate_ExpiringWithRate(t *testing.T) {
	t.Parallel()
	in := unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: 0.5}, noInFlight())
	in.LayerStates = withExpiring(withExisting(in.LayerStates, LayerComputeSP, 4), LayerComputeSP, 2)
	a := mustAllocate(t, in).Allocations[0] // E = 4-2 = 2, OD gap 8
	ratEq(t, "od", a.OnDemandEquivalentUSDPerHour, 8, 1)
	ratEq(t, "commitment", a.GapUSDPerHour, 4, 1)
}

// mixedInput reproduces TestAllocate_AWS3LayerSplit (OD split: base 3, buffer
// 9/2, flex 3/2) with a different exact rate per layer.
func mixedInput(cap *float64) *AllocationInput {
	rates := map[LayerType]float64{LayerEC2InstanceSP: 0.5, LayerConvertibleRI: 0.25, LayerComputeSP: 0.75}
	in := unitsInput(10, 0.5, rates, noInFlight())
	in.Baseline.StableUSDPerHour = ptr(4.0)
	in.LayerStates = withExisting(in.LayerStates, LayerEC2InstanceSP, 1)
	in.Config.MaxHourlyCommitPerRun = cap
	return in
}

func TestAllocate_MixedRatesSplitInODConvertPerLayer(t *testing.T) {
	t.Parallel()
	m := allocByLayer(mustAllocate(t, mixedInput(nil)))
	ratEq(t, "base od", m[LayerEC2InstanceSP].OnDemandEquivalentUSDPerHour, 3, 1)
	ratEq(t, "base c", m[LayerEC2InstanceSP].GapUSDPerHour, 3, 2)
	ratEq(t, "buffer od", m[LayerConvertibleRI].OnDemandEquivalentUSDPerHour, 9, 2)
	ratEq(t, "buffer c", m[LayerConvertibleRI].GapUSDPerHour, 9, 8)
	ratEq(t, "flex od", m[LayerComputeSP].OnDemandEquivalentUSDPerHour, 3, 2)
	ratEq(t, "flex c", m[LayerComputeSP].GapUSDPerHour, 9, 8)
}

func TestAllocate_CapIsInCommitmentDollars(t *testing.T) {
	t.Parallel()
	// Uncapped commitment total = 3/2 + 9/8 + 9/8 = 15/4; cap 3 scales by 4/5.
	res := mustAllocate(t, mixedInput(ptr(3.0)))
	total := new(big.Rat)
	for _, a := range res.Allocations {
		total.Add(total, a.GapUSDPerHour)
		wantOD := new(big.Rat).Quo(a.GapUSDPerHour, a.Rate)
		if wantOD.Cmp(a.OnDemandEquivalentUSDPerHour) != 0 {
			t.Errorf("%s: od %s != commitment/rate %s after cap", a.Layer, a.OnDemandEquivalentUSDPerHour, wantOD)
		}
	}
	ratEq(t, "capped total", total, 3, 1)
}

func TestAllocate_MissingRateHoldsOnlyThatLayer(t *testing.T) {
	t.Parallel()
	rates := map[LayerType]float64{LayerEC2InstanceSP: 0.5, LayerConvertibleRI: 0.25} // no flex
	res := mustAllocate(t, mixedInputWithRates(rates))
	m := allocByLayer(res)
	if _, ok := m[LayerComputeSP]; ok {
		t.Fatalf("flex must not be allocated without a rate: %+v", res.Allocations)
	}
	ratEq(t, "base c unchanged, not re-routed", m[LayerEC2InstanceSP].GapUSDPerHour, 3, 2)
	ratEq(t, "buffer c unchanged, not re-routed", m[LayerConvertibleRI].GapUSDPerHour, 9, 8)
	var found bool
	for _, h := range res.Holds {
		if h.Layer == LayerComputeSP && strings.Contains(h.Rationale, "no commitment rate") && h.Action == ActionHold {
			found = true
		}
	}
	if !found {
		t.Errorf("want a no-commitment-rate Hold for %s, got %+v", LayerComputeSP, res.Holds)
	}
}

func mixedInputWithRates(rates map[LayerType]float64) *AllocationInput {
	in := mixedInput(nil)
	in.CommitmentRates = rates
	return in
}

func TestAllocate_NoRateAtAllIsHoldNeverOneToOne(t *testing.T) {
	t.Parallel()
	res := mustAllocate(t, unitsInput(10, 0, map[LayerType]float64{}, noInFlight()))
	if !res.IsNoOp() || len(res.Holds) != 1 || !strings.Contains(res.Holds[0].Rationale, "never assumed 1:1") {
		t.Fatalf("result = %+v, want one no-rate Hold and no allocation", res)
	}
}

func TestAllocate_MissingRateOnIdleLayerIsFine(t *testing.T) {
	t.Parallel()
	// Only flex receives an allocation (no base stable level, no buffer).
	res := mustAllocate(t, unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: 0.5}, noInFlight()))
	if len(res.Allocations) != 1 || len(res.Holds) != 0 {
		t.Fatalf("result = %+v, want exactly one allocation and no holds", res)
	}
}

func TestAllocate_InvalidRatesAreErrors(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{math.NaN(), math.Inf(1), 0, -0.5, 1.0000001, 2} {
		in := unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: v}, noInFlight())
		if res, err := Allocate(in); err == nil {
			t.Errorf("rate %g: Allocate = %+v, want error", v, res)
		}
	}
	// A bad rate on a layer that receives nothing is still a data error.
	in := unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: 0.5, LayerConvertibleRI: 7}, noInFlight())
	if _, err := Allocate(in); err == nil {
		t.Error("invalid rate on an idle layer must still fail loud")
	}
}

func TestAllocate_NilRatesRejected(t *testing.T) {
	t.Parallel()
	_, err := Allocate(unitsInput(10, 0, nil, noInFlight()))
	if err == nil || !strings.Contains(err.Error(), "CommitmentRates must not be nil") {
		t.Fatalf("err = %v, want CommitmentRates nil rejection", err)
	}
}

func TestAllocate_NilBaselineRejectsInvalidRates(t *testing.T) {
	t.Parallel()
	for name, rate := range map[string]float64{
		"NaN": math.NaN(), "zero": 0, "above one": 1.5,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: rate}, noInFlight())
			in.Baseline.LowWaterUSDPerHour = nil
			in.LayerStates = withBufferUtilization(withExisting(in.LayerStates, LayerConvertibleRI, 2), 70)
			res, err := Allocate(in)
			if err == nil || !strings.Contains(err.Error(), "commitment_rates") || res != nil {
				t.Fatalf("Allocate = %+v, %v; want rate error and no maintenance result", res, err)
			}
		})
	}
}

func TestAllocate_NilBaselineRejectsInvalidInFlight(t *testing.T) {
	t.Parallel()
	for name, tr := range map[string]InFlightTranche{
		"zero stored rate":      {Layer: LayerComputeSP, CommitmentUSDPerHour: 1},
		"NaN stored rate":       {Layer: LayerComputeSP, CommitmentUSDPerHour: 1, Rate: math.NaN()},
		"stored rate above one": {Layer: LayerComputeSP, CommitmentUSDPerHour: 1, Rate: 1.5},
		"negative amount":       {Layer: LayerComputeSP, CommitmentUSDPerHour: -1, Rate: 0.5},
		"unknown layer":         {Layer: LayerAzureSavingsPlan, CommitmentUSDPerHour: 1, Rate: 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := unitsInput(10, 0, map[LayerType]float64{LayerComputeSP: 0.5}, []InFlightTranche{tr})
			in.Baseline.LowWaterUSDPerHour = nil
			in.LayerStates = withBufferUtilization(withExisting(in.LayerStates, LayerConvertibleRI, 2), 70)
			res, err := Allocate(in)
			if err == nil || !strings.Contains(err.Error(), "in_flight[0]") || res != nil {
				t.Fatalf("Allocate = %+v, %v; want in-flight error and no maintenance result", res, err)
			}
		})
	}
}

func TestAllocate_NilBaselineMaintainsBufferWithPartialOrEmptyRates(t *testing.T) {
	t.Parallel()
	for name, rates := range map[string]map[LayerType]float64{
		"partial": {LayerComputeSP: 0.5}, "empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := unitsInput(10, 0, rates, noInFlight())
			in.Baseline.LowWaterUSDPerHour = nil
			in.LayerStates = withBufferUtilization(withExisting(in.LayerStates, LayerConvertibleRI, 2), 70)
			res := mustAllocate(t, in)
			if len(res.Allocations) != 0 || len(res.Reshapes) != 1 || res.Reshapes[0].Layer != LayerConvertibleRI || len(res.Holds) != 1 || !strings.Contains(res.Holds[0].Rationale, "baseline unavailable") {
				t.Fatalf("Allocate = %+v; want buffer reshape and baseline hold", res)
			}
		})
	}
}

func TestAllocate_InvalidInFlightIsError(t *testing.T) {
	t.Parallel()
	rates := map[LayerType]float64{LayerComputeSP: 0.5}
	for name, tr := range map[string]InFlightTranche{
		"zero rate (unset)": {Layer: LayerComputeSP, CommitmentUSDPerHour: 1},
		"rate above 1":      {Layer: LayerComputeSP, CommitmentUSDPerHour: 1, Rate: 1.5},
		"NaN rate":          {Layer: LayerComputeSP, CommitmentUSDPerHour: 1, Rate: math.NaN()},
		"negative amount":   {Layer: LayerComputeSP, CommitmentUSDPerHour: -1, Rate: 0.5},
		"NaN amount":        {Layer: LayerComputeSP, CommitmentUSDPerHour: math.NaN(), Rate: 0.5},
		"unknown layer":     {Layer: LayerAzureSavingsPlan, CommitmentUSDPerHour: 1, Rate: 0.5},
	} {
		if res, err := Allocate(unitsInput(10, 0, rates, []InFlightTranche{tr})); err == nil {
			t.Errorf("%s: Allocate = %+v, want error", name, res)
		}
	}
}

// TestAllocate_BaseSplitCountsInFlightBase pins amendment 8: base in-flight
// counts toward the stable level, or base can overshoot stable while earlier
// base tranches wait to fire.
func TestAllocate_BaseSplitCountsInFlightBase(t *testing.T) {
	t.Parallel()
	rates := map[LayerType]float64{LayerEC2InstanceSP: 0.5, LayerComputeSP: 0.5}
	// 1.5 commitment @ 0.5 = 3 OD in flight on base; stable 4 => 1 OD left.
	inflight := []InFlightTranche{{Layer: LayerEC2InstanceSP, CommitmentUSDPerHour: 1.5, Rate: 0.5}}
	in := unitsInput(10, 0, rates, inflight)
	in.Baseline.StableUSDPerHour = ptr(4.0)
	m := allocByLayer(mustAllocate(t, in))
	ratEq(t, "base od", m[LayerEC2InstanceSP].OnDemandEquivalentUSDPerHour, 1, 1) // old math: 4
	ratEq(t, "flex od", m[LayerComputeSP].OnDemandEquivalentUSDPerHour, 6, 1)     // gap 10-3=7, minus base 1
}

func TestAllocate_SubMinimumAfterConversionIsDropped(t *testing.T) {
	t.Parallel()
	// OD gap 0.015 > 0.01 passes the target-met check; commitment 0.0075 < 0.01.
	res := mustAllocate(t, unitsInput(0.015, 0, map[LayerType]float64{LayerComputeSP: 0.5}, noInFlight()))
	if len(res.Allocations) != 0 || len(res.Holds) != 1 || !strings.Contains(res.Holds[0].Rationale, "below minimum") {
		t.Fatalf("result = %+v, want the sub-minimum commitment dropped with a Hold", res)
	}
}

// TestAllocate_UnitProperties checks the exact-arithmetic invariants over a
// grid of random OD gaps, buffer fractions and rates (all dyadic so float
// inputs are exact rationals), then feeds the allocations to BuildTranches.
func TestAllocate_UnitProperties(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(349))
	dyadic := func() float64 { return float64(1+rng.Intn(16)) / 16 } // (0, 1]
	for i := 0; i < 300; i++ {
		rates := map[LayerType]float64{LayerEC2InstanceSP: dyadic(), LayerComputeSP: dyadic(), LayerConvertibleRI: dyadic()}
		in := unitsInput(float64(1+rng.Intn(4000))/16, float64(rng.Intn(4))/4, rates, noInFlight())
		in.Baseline.StableUSDPerHour = ptr(float64(rng.Intn(2000)) / 16)
		res := mustAllocate(t, in)
		sumOD := new(big.Rat)
		for _, a := range res.Allocations {
			r := new(big.Rat).SetFloat64(rates[a.Layer])
			want := new(big.Rat).Mul(a.OnDemandEquivalentUSDPerHour, r)
			if a.GapUSDPerHour.Cmp(want) != 0 || a.Rate.Cmp(r) != 0 {
				t.Fatalf("iter %d %s: c=%s, want od*r=%s", i, a.Layer, a.GapUSDPerHour, want)
			}
			if a.GapUSDPerHour.Cmp(a.OnDemandEquivalentUSDPerHour) > 0 {
				t.Fatalf("iter %d %s: commitment %s exceeds od %s", i, a.Layer, a.GapUSDPerHour, a.OnDemandEquivalentUSDPerHour)
			}
			sumOD.Add(sumOD, new(big.Rat).Quo(a.GapUSDPerHour, a.Rate))
		}
		assertTrancheConservation(t, i, in, res, sumOD)
	}
}

// assertTrancheConservation checks the exactly-once conversion: the tranche
// amounts sum to the allocation commitments, each tranche carries its rate,
// and converting back with the stored rates recovers the OD gap.
func assertTrancheConservation(t *testing.T, iter int, in *AllocationInput, res *AllocateResult, sumOD *big.Rat) {
	t.Helper()
	if len(res.Allocations) == 0 {
		return
	}
	cfg := in.Config
	cfg.Ramp = RampSchedule{Steps: []RampStep{{AfterDays: 0, Fraction: 0.5}, {AfterDays: 7, Fraction: 0.5}}}
	tr, err := BuildTranches(&TrancheInput{
		Config: &cfg, RunID: "r", Term: Term1Year, PaymentOption: PaymentNoUpfront,
		NewID: seqID("t"), Now: in.Now, Allocations: res.Allocations,
	})
	if err != nil {
		t.Fatalf("iter %d BuildTranches: %v", iter, err)
	}
	sumC, backOD := new(big.Rat), new(big.Rat)
	for _, tranche := range tr.Tranches {
		c, _ := new(big.Rat).SetString(tranche.AmountUSDPerHour)
		r, ok := new(big.Rat).SetString(tranche.CommitmentRate)
		if !ok {
			t.Fatalf("iter %d: tranche %s has no commitment rate", iter, tranche.ID)
		}
		sumC.Add(sumC, c)
		backOD.Add(backOD, new(big.Rat).Quo(c, r))
	}
	wantC := new(big.Rat)
	for _, a := range res.Allocations {
		wantC.Add(wantC, a.GapUSDPerHour)
	}
	if sumC.Cmp(wantC) != 0 || backOD.Cmp(sumOD) != 0 {
		t.Fatalf("iter %d: tranche sum %s (want %s), od round-trip %s (want %s)", iter, sumC, wantC, backOD, sumOD)
	}
}

// TestAllocate_LowerRateNeverRaisesCommitment: for a fixed OD gap, the
// commitment is monotone in r and at most the OD gap.
func TestAllocate_LowerRateNeverRaisesCommitment(t *testing.T) {
	t.Parallel()
	prev := new(big.Rat)
	for k := 1; k <= 16; k++ {
		rates := map[LayerType]float64{LayerComputeSP: float64(k) / 16}
		c := mustAllocate(t, unitsInput(10, 0, rates, noInFlight())).Allocations[0].GapUSDPerHour
		if c.Cmp(prev) <= 0 {
			t.Fatalf("r=%d/16: commitment %s not above previous %s", k, c, prev)
		}
		prev = c
	}
	ratEq(t, "r=1 commitment", prev, 10, 1)
}

func TestAllocate_CappedTotalNeverExceedsCap(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 100; i++ {
		capUSD := float64(1+rng.Intn(64)) / 16
		res := mustAllocate(t, mixedInput(&capUSD))
		total := new(big.Rat)
		for _, a := range res.Allocations {
			total.Add(total, a.GapUSDPerHour)
		}
		if total.Cmp(new(big.Rat).SetFloat64(capUSD)) > 0 {
			t.Fatalf("cap %g: total commitment %s exceeds cap", capUSD, total)
		}
	}
}

func TestBuildTranches_RejectsUnconvertedAllocation(t *testing.T) {
	t.Parallel()
	for name, rate := range map[string]*big.Rat{
		"nil": nil, "zero": new(big.Rat), "negative": big.NewRat(-1, 2), "above one": big.NewRat(3, 2),
	} {
		a := mkAlloc(LayerComputeSP, 5)
		a.Rate = rate
		in := baseInput(baseConfig(threeStepRamp()), []Allocation{a}, nowFixed())
		_, err := BuildTranches(in)
		if err == nil || !strings.Contains(err.Error(), "rate must be in (0, 1]") {
			t.Errorf("%s: err = %v, want rate rejection", name, err)
		}
	}
}

func TestTranche_ValidateCommitmentRate(t *testing.T) {
	t.Parallel()
	for rate, wantErr := range map[string]bool{
		"": false, "7/10": false, "1": false, "0.5": false,
		"0": true, "-1/2": true, "11/10": true, "abc": true,
	} {
		tr := validTrancheForRate()
		tr.CommitmentRate = rate
		if err := tr.Validate(); (err != nil) != wantErr {
			t.Errorf("rate %q: err = %v, wantErr %v", rate, err, wantErr)
		}
	}
}

func validTrancheForRate() Tranche {
	return Tranche{
		ID: "t1", RunID: "r1", FireAfter: nowFixed(), Status: TrancheStatusScheduled,
		AmountUSDPerHour: "7", Layer: LayerComputeSP, Term: Term1Year, PaymentOption: PaymentNoUpfront,
	}
}
