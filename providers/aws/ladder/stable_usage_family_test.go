package ladder

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFamilySource serves a fixed per-family series or a fixed error.
type fakeFamilySource struct {
	err      error
	families map[string][]DailyPoint
}

func (f *fakeFamilySource) GetOnDemandFamilySeries(_ context.Context, _ string, _ int) (map[string][]DailyPoint, error) {
	return f.families, f.err
}

// TestFamilyStable_NeverExceedsSumOfFamilyMinimums: with two or more families
// the estimate is the sum over families of min(series) * (1 - f), capped by the
// flat estimate. The flat value here is large so the family sum is what binds.
func TestFamilyStable_NeverExceedsSumOfFamilyMinimums(t *testing.T) {
	rng := rand.New(rand.NewPCG(731, 1))
	const f = 0.1
	flat := ptr(1e9)
	for iter := 0; iter < 200; iter++ {
		n := 2 + rng.IntN(3)
		families := make(map[string][]float64, n)
		sumMin := 0.0
		for i := 0; i < n; i++ {
			series := make([]float64, 14+rng.IntN(20))
			for j := range series {
				series[j] = 50 + rng.Float64()*20
			}
			families[string(rune('a'+i))] = series
			sumMin += minOf(series) * (1 - f)
		}
		got, _, err := familyStableSplit(families, flat, f)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.LessOrEqual(t, *got, sumMin+1e-9, "iteration %d: estimate exceeds sum of family minima", iter)
		assert.InDelta(t, sumMin, *got, 1e-9, "iteration %d: family sum must equal sum of minima when flat does not bind", iter)
	}
}

// TestFamilyStable_CappedByFlat: when the flat estimate is smaller, it wins.
func TestFamilyStable_CappedByFlat(t *testing.T) {
	families := map[string][]float64{"m5": steadySeries(10, 14), "c5": steadySeries(4, 14)}
	got, _, err := familyStableSplit(families, ptr(2.0), 0.1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.InDelta(t, 2.0, *got, 1e-9)
}

// steadySeries returns n equal daily values: it passes the stable gate.
func steadySeries(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// TestFamilyStable_VolatileFamilyContributesNothing: a family that fails the
// gate (p10 well below half its median) adds nothing, so only the steady family
// counts. Summing its minimum would give 9.9 instead of 9.
func TestFamilyStable_VolatileFamilyContributesNothing(t *testing.T) {
	volatile := append([]float64{1, 1, 1, 1, 1}, steadySeries(100, 9)...)
	families := map[string][]float64{"m5": steadySeries(10, 14), "c5": volatile}
	got, _, err := familyStableSplit(families, ptr(1e9), 0.1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.InDelta(t, 9.0, *got, 1e-9, "10 * 0.9 from the steady family only")
}

// TestFamilyStable_ShortFamilyContributesNothing: a 13-day family fails the
// minimum-length gate and adds nothing.
func TestFamilyStable_ShortFamilyContributesNothing(t *testing.T) {
	families := map[string][]float64{"m5": steadySeries(10, 14), "c5": steadySeries(4, 13)}
	got, _, err := familyStableSplit(families, ptr(1e9), 0.1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.InDelta(t, 9.0, *got, 1e-9, "10 * 0.9 from the steady family only")
}

// TestFamilyStable_AllFamiliesFailGateReturnsNil: when no family passes, there
// is no stable claim, so the base layer stays off even with a flat estimate.
func TestFamilyStable_AllFamiliesFailGateReturnsNil(t *testing.T) {
	volatile := append([]float64{1, 1, 1, 1, 1}, steadySeries(100, 9)...)
	families := map[string][]float64{"c5": volatile, "m5": steadySeries(4, 13)}
	got, _, err := familyStableSplit(families, ptr(5.0), 0.1)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestFamilyStable_GateBoundaryPasses: p10 exactly half the median passes the
// gate (">="). Here p10 = 5, median = 10, min = 5, so the total is
// (10 + 5) * 0.9 = 13.5.
func TestFamilyStable_GateBoundaryPasses(t *testing.T) {
	boundary := append([]float64{5, 5}, steadySeries(10, 12)...)
	families := map[string][]float64{"m5": steadySeries(10, 14), "c5": boundary}
	got, _, err := familyStableSplit(families, ptr(1e9), 0.1)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.InDelta(t, 13.5, *got, 1e-9)
}

// TestFamilyStable_NoFamilyDataHasNoBase: no family rows means no family-bound
// base, even when the flat estimate exists.
func TestFamilyStable_NoFamilyDataHasNoBase(t *testing.T) {
	got, _, err := familyStableSplit(map[string][]float64{}, ptr(7.5), 0.1)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestFamilyStable_NilFlatStaysNil: a volatile or short flat series keeps the
// base layer off; per-family data cannot switch it on.
func TestFamilyStable_NilFlatStaysNil(t *testing.T) {
	families := map[string][]float64{"m5": steadySeries(10, 14), "c5": steadySeries(4, 14)}
	got, _, err := familyStableSplit(families, nil, 0.1)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestFamilyStable_SingleFamilyMatchesFlat: one family is the flat series, so
// the result equals the existing estimator exactly.
func TestFamilyStable_SingleFamilyMatchesFlat(t *testing.T) {
	series := make([]float64, 30)
	for i := range series {
		series[i] = float64(10 + i%7)
	}
	const p, f = 5.0, 0.1
	lowWater := nearestRank(t, series, p)
	flat, err := estimateStableUSDPerHour(series, lowWater, f)
	require.NoError(t, err)
	got, _, err := familyStableSplit(map[string][]float64{"m5": series}, flat, f)
	require.NoError(t, err)
	assert.Equal(t, flat, got)
}

// TestFamilyStable_RejectsBadFamilySeries: fail loud on empty, negative or
// non-finite family values.
func TestFamilyStable_RejectsBadFamilySeries(t *testing.T) {
	flat := ptr(5.0)
	cases := map[string]map[string][]float64{
		"empty":    {"m5": {}, "c5": {4}},
		"negative": {"m5": {1, -1}, "c5": {4, 4}},
		"nan":      {"m5": {1, 1}, "c5": {4, nanValue()}},
	}
	for name, fams := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := familyStableSplit(fams, flat, 0.1)
			require.Error(t, err)
		})
	}
}

// TestGetUsageBaseline_FamilyPathSetsStable: the family path replaces the flat
// stable value end to end, and a family fetch error withholds the base.
func TestGetUsageBaseline_FamilyPathSetsStable(t *testing.T) {
	const days = 30
	yesterday := utcDay(time.Now()).AddDate(0, 0, -1)
	series := func(v func(i int) float64) []DailyPoint {
		out := make([]DailyPoint, days)
		for i := range out {
			out[i] = DailyPoint{Date: yesterday.AddDate(0, 0, -(days - 1 - i)), USDPerHour: v(i)}
		}
		return out
	}
	// m5 dips to 1 on the last day; c5 is flat at 4. Family minima sum to 5.
	fams := map[string][]DailyPoint{
		"m5": series(func(i int) float64 {
			if i == days-1 {
				return 1
			}
			return 10
		}),
		"c5": series(func(int) float64 { return 4 }),
	}
	// Flat is min(14, 14, 9) * 0.9 = 8.1, above the family total 4.5, so the
	// assertion below fails if the family path is not used.
	flat := series(func(i int) float64 { return 14 })
	flat[days-1].USDPerHour = 9
	cov := &fakeCoverageSource{onDemandPoints: flat}
	a := newTestLadder(t, &fakeRILister{}, &fakeSPLister{}, cov, &fakeUtilizationSource{})
	_, err := a.WithBufferFraction(0.1)
	require.NoError(t, err)

	a.family = &fakeFamilySource{families: fams}
	bl, err := a.GetUsageBaseline(context.Background(), testScope(), days, 5)
	require.NoError(t, err)
	require.NotNil(t, bl.StableUSDPerHour)
	assert.InDelta(t, 4.5, *bl.StableUSDPerHour, 1e-9, "(1 + 4) * 0.9, below flat 8.1")
	// m5 contributes 0.9 and c5 3.6. Their total 4.5 is below flat 8.1, so the
	// base is not scaled and the shares are those contributions.
	require.Len(t, bl.StableUSDPerHourByFamily, 2)
	assert.InDelta(t, 0.9, bl.StableUSDPerHourByFamily["m5"], 1e-9)
	assert.InDelta(t, 3.6, bl.StableUSDPerHourByFamily["c5"], 1e-9)

	a.family = nil
	flatOnly, err := a.GetUsageBaseline(context.Background(), testScope(), days, 5)
	require.NoError(t, err)
	require.NotNil(t, flatOnly.StableUSDPerHour)

	a.family = &fakeFamilySource{err: errors.New("ce down")}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	bl, err = a.GetUsageBaseline(context.Background(), testScope(), days, 5)
	assert.Contains(t, logs.String(), "WARNING")
	assert.Contains(t, logs.String(), "GetOnDemandFamilySeries")
	require.NoError(t, err, "a family fetch error must not fail the baseline")
	assert.Nil(t, bl.StableUSDPerHour, "no family data means no family-bound base")
	assert.Nil(t, bl.StableUSDPerHourByFamily)
}

func minOf(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}

// nearestRank is the test-side percentile used to derive lowWater the same way
// GetUsageBaseline does.
func nearestRank(t *testing.T, series []float64, p float64) float64 {
	t.Helper()
	v, err := nearestRankPercentile(series, p)
	require.NoError(t, err)
	return v
}
