package ladder

import (
	"context"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// rep returns n copies of v.
func rep(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// estimate derives lowWater from the series at percentile p, as GetUsageBaseline does.
func estimate(t *testing.T, series []float64, p, f float64) *float64 {
	t.Helper()
	lw, err := nearestRankPercentile(series, p)
	require.NoError(t, err)
	got, err := estimateStableUSDPerHour(series, lw, f)
	require.NoError(t, err)
	return got
}

func TestEstimateStableUSDPerHour_Golden(t *testing.T) {
	t.Parallel()
	const f = 0.1
	tests := []struct {
		name   string
		series []float64
		p      float64
		f      float64
		want   float64 // 0 means nil
	}{
		{"a stable series", rep(30, 10), 5, f, 9.0},
		{"g zero fraction applies no haircut", rep(30, 10), 5, 0, 10.0},
		{"b volatile: 10% of days at 1", slices.Concat(rep(3, 1), rep(27, 10)), 5, f, 0},
		{"c 13 points is short", rep(13, 10), 5, f, 0},
		{"c 14 points is enough", rep(14, 10), 5, f, 9.0},
		{"d floor exactly half the median passes", slices.Concat(rep(3, 5.0), rep(27, 10)), 5, f, 4.5},
		{"d floor just under half the median is nil", slices.Concat(rep(3, 4.999), rep(27, 10)), 5, f, 0},
		// P10 is rank 3 (8); any floor percentile giving rank 3 is equivalent.
		{"e1 floor percentile binds below lowWater", slices.Concat([]float64{6, 7, 8}, rep(27, 10)), 50, f, 7.2},
		{"e2 lowWater clamp binds below P10", slices.Concat([]float64{7, 7, 9}, rep(27, 10)), 5, f, 6.3},
		{"e3 max percentile never exceeds the body", slices.Concat(rep(29, 10), []float64{20}), 100, f, 9.0},
		{"e4 week-long decline sizes on the low level", slices.Concat(rep(23, 10), rep(7, 6)), 50, f, 5.4},
		{"e5b third-from-last day zero is nil", slices.Concat(rep(27, 10), []float64{0, 10, 10}), 5, f, 0},
		{"e5b fourth-from-last day zero is outside the window", slices.Concat(rep(26, 10), []float64{0, 10, 10, 10}), 5, f, 9.0},
		{"e5i last day zero is nil", slices.Concat(rep(29, 10), []float64{0}), 5, f, 0},
		{"e5ii last two days zero is nil at p50", slices.Concat(rep(28, 10), rep(2, 0)), 50, f, 0},
		{"e5iii last three days zero is nil", slices.Concat(rep(27, 10), rep(3, 0)), 50, f, 0},
		{"e6 all zeros is nil", rep(30, 0), 5, f, 0},
		{"e7 even N uses the lower median", slices.Concat(rep(7, 4), rep(7, 10)), 5, f, 3.6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := estimate(t, tt.series, tt.p, tt.f)
			if tt.want == 0 {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.InDelta(t, tt.want, *got, 1e-9)
		})
	}
}

func TestEstimateStableUSDPerHour_InvalidInputsAreNil(t *testing.T) {
	t.Parallel()
	ok := rep(30, 10)
	tests := []struct {
		name     string
		series   []float64
		lowWater float64
		f        float64
	}{
		{"empty", nil, 10, 0.1},
		{"NaN element", slices.Concat(rep(29, 10), []float64{math.NaN()}), 10, 0.1},
		{"Inf element", slices.Concat(rep(29, 10), []float64{math.Inf(1)}), 10, 0.1},
		{"negative element", slices.Concat(rep(29, 10), []float64{-1}), 10, 0.1},
		{"NaN lowWater", ok, math.NaN(), 0.1},
		{"Inf lowWater", ok, math.Inf(1), 0.1},
		{"fraction one", ok, 10, 1},
		{"fraction negative", ok, 10, -0.5},
		{"fraction above one", ok, 10, 1.5},
		{"fraction NaN", ok, 10, math.NaN()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := estimateStableUSDPerHour(tt.series, tt.lowWater, tt.f)
			require.NoError(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestEstimateStableUSDPerHour_Property(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // Deterministic test data, not security.
	percentiles := []float64{1, 5, 10, 50, 100}
	nonNil := 0
	for range 1000 {
		series := make([]float64, 14+rng.IntN(47))
		for i := range series {
			series[i] = 10 + float64(rng.IntN(61)-30)/10 // Steady: 10 plus noise in [-3, 3].
		}
		p := percentiles[rng.IntN(len(percentiles))]
		f := rng.Float64()
		lw, err := nearestRankPercentile(series, p)
		require.NoError(t, err)
		p10, err := nearestRankPercentile(series, 10)
		require.NoError(t, err)
		min3 := slices.Min(series[len(series)-3:])

		got, err := estimateStableUSDPerHour(series, lw, f)
		require.NoError(t, err)
		if got == nil {
			continue
		}
		nonNil++
		assert.Greater(t, *got, 0.0)
		assert.LessOrEqual(t, *got, lw*(1-f)+1e-12)
		assert.LessOrEqual(t, *got, p10*(1-f)+1e-12)
		assert.LessOrEqual(t, *got, min3*(1-f)+1e-12)
	}
	assert.Greater(t, nonNil, 500, "generator must mostly pass the gate or the checks are vacuous")
}

// stablePublic runs GetUsageBaseline on vals (oldest first); f nil leaves the option unset.
func stablePublic(t *testing.T, vals []float64, lookback int, f *float64) (*float64, *float64) {
	t.Helper()
	points := makeRecentPoints(len(vals))
	for i := range points {
		points[i].USDPerHour = vals[i]
	}
	a := newTestLadder(t, &fakeRILister{}, &fakeSPLister{}, &fakeCoverageSource{onDemandPoints: points}, &fakeUtilizationSource{})
	if f != nil {
		_, err := a.WithBufferFraction(*f)
		require.NoError(t, err)
	}
	bl, err := a.GetUsageBaseline(context.Background(), testScope(), lookback, 5)
	require.NoError(t, err)
	return bl.StableUSDPerHour, bl.LowWaterUSDPerHour
}

func TestGetUsageBaseline_StableUsage(t *testing.T) {
	t.Parallel()
	f := 0.1

	t.Run("h option unset keeps stable nil on a qualifying series", func(t *testing.T) {
		t.Parallel()
		stable, _ := stablePublic(t, rep(30, 10), 30, nil)
		assert.Nil(t, stable)
	})
	t.Run("option set yields the haircut estimate, not aliased to lowWater", func(t *testing.T) {
		t.Parallel()
		stable, low := stablePublic(t, rep(30, 10), 30, &f)
		require.NotNil(t, stable)
		assert.InDelta(t, 9.0, *stable, 1e-9)
		assert.NotSame(t, low, stable)
	})
	t.Run("volatile series is nil", func(t *testing.T) {
		t.Parallel()
		stable, _ := stablePublic(t, slices.Concat(rep(3, 1), rep(27, 10)), 30, &f)
		assert.Nil(t, stable)
	})
	t.Run("short series is nil, 14 days is enough", func(t *testing.T) {
		t.Parallel()
		stable, _ := stablePublic(t, rep(13, 10), 13, &f)
		assert.Nil(t, stable)
		stable, _ = stablePublic(t, rep(14, 10), 14, &f)
		require.NotNil(t, stable)
		assert.InDelta(t, 9.0, *stable, 1e-9)
	})
}

// allocateFromBaseline feeds a real GetUsageBaseline result to the engine with
// no existing commitment and a unit rate on every layer.
func allocateFromBaseline(t *testing.T, vals []float64, f float64) *ladder.AllocateResult {
	t.Helper()
	points := makeRecentPoints(len(vals))
	for i := range points {
		points[i].USDPerHour = vals[i]
	}
	a := newTestLadder(t, &fakeRILister{}, &fakeSPLister{}, &fakeCoverageSource{onDemandPoints: points}, &fakeUtilizationSource{})
	_, err := a.WithBufferFraction(f)
	require.NoError(t, err)
	bl, err := a.GetUsageBaseline(context.Background(), testScope(), len(vals), 5)
	require.NoError(t, err)

	zero := 0.0
	states := map[ladder.LayerType]ladder.LayerState{}
	rates := map[ladder.LayerType]float64{}
	for _, spec := range a.SupportedLayers() {
		states[spec.Type] = ladder.LayerState{Layer: spec.Type, ExistingUSDPerHour: &zero, ExpiringUSDPerHour: &zero}
		rates[spec.Type] = 1
	}
	res, err := ladder.Allocate(&ladder.AllocationInput{
		Config: ladder.LadderConfig{Scope: testScope(), Mode: ladder.ModeEmailApproval, Cadence: ladder.CadenceWeekly,
			TargetCoveragePct: 100, BaselinePercentile: 5, LookbackDays: len(vals), MaxActionsPerRun: 10,
			BufferFraction:                f,
			BufferUtilizationThresholdPct: ladder.DefaultBufferUtilizationThresholdPct,
			Ramp:                          ladder.RampSchedule{Steps: []ladder.RampStep{{AfterDays: 0, Fraction: 1}}}},
		Layers: a.SupportedLayers(), LayerStates: states, CommitmentRates: rates, InFlight: []ladder.InFlightTranche{},
		Baseline: bl, Now: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), DataSources: []string{"constructed"},
	})
	require.NoError(t, err)
	return res
}

func TestStableUsage_EndToEndAllocate(t *testing.T) {
	t.Parallel()
	t.Run("stable series caps the base layer at Stable", func(t *testing.T) {
		t.Parallel()
		// min3 = 6 gives Stable 5.4 < coreGap 9, so the cap binds.
		res := allocateFromBaseline(t, slices.Concat(rep(27, 10), []float64{6, 10, 10}), 0.1)
		got := map[ladder.LayerType]*big.Rat{}
		for _, al := range res.Allocations {
			got[al.Layer] = al.OnDemandEquivalentUSDPerHour
		}
		require.Len(t, got, 3)
		// Amounts derive from float64 inputs (0.9 * 6), so compare within 1e-12.
		base, _ := got[ladder.LayerEC2InstanceSP].Float64()
		assert.InDelta(t, 5.4, base, 1e-12)
		flex, _ := got[ladder.LayerComputeSP].Float64()
		assert.InDelta(t, 3.6, flex, 1e-12)
		buffer, _ := got[ladder.LayerConvertibleRI].Float64()
		assert.InDelta(t, 1.0, buffer, 1e-12)
	})
	t.Run("volatile series routes the whole core gap to flex", func(t *testing.T) {
		t.Parallel()
		res := allocateFromBaseline(t, slices.Concat(rep(3, 1), rep(27, 10)), 0.1)
		noted := false
		for _, al := range res.Allocations {
			assert.NotEqual(t, ladder.LayerEC2InstanceSP, al.Layer)
			noted = noted || strings.Contains(al.Rationale, "stable usage unknown")
		}
		assert.True(t, noted, "flex allocation should carry the stable-usage-unknown note")
	})
}
