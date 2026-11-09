package ladder

import (
	"context"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	for range 1000 {
		series := make([]float64, 14+rng.IntN(47))
		for i := range series {
			series[i] = float64(rng.IntN(200)) / 10
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
		assert.Greater(t, *got, 0.0)
		assert.LessOrEqual(t, *got, lw*(1-f)+1e-12)
		assert.LessOrEqual(t, *got, p10*(1-f)+1e-12)
		assert.LessOrEqual(t, *got, min3*(1-f)+1e-12)
	}
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
