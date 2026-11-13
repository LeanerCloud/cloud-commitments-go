package ladder

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFamilySplit_FollowsMinimums: each family's share is its contribution
// (minimum * (1 - f)) over the total contribution, so a family with three times
// the minimum gets three times the share.
func TestFamilySplit_FollowsMinimums(t *testing.T) {
	const f = 0.1
	families := map[string][]float64{
		"m5": steadySeries(10, 14),
		"c5": steadySeries(30, 14),
	}
	// Contributions are 9 and 27 (total 36). Flat 18 caps the base, so the
	// shares scale down to 4.5 and 13.5.
	base, shares, err := familyStableSplit(families, ptr(18), f)
	require.NoError(t, err)
	require.NotNil(t, base)
	assert.InDelta(t, 18, *base, 1e-9)
	assert.InDelta(t, 4.5, shares["m5"], 1e-9)
	assert.InDelta(t, 13.5, shares["c5"], 1e-9)

	_, uncapped, err := familyStableSplit(families, ptr(1e9), f)
	require.NoError(t, err)
	assert.InDelta(t, 9, uncapped["m5"], 1e-9)
	assert.InDelta(t, 27, uncapped["c5"], 1e-9)
}

// TestFamilySplit_SumsToBase: shares sum to the base estimate and no share
// exceeds it, across random families and both capped and uncapped flat values.
func TestFamilySplit_SumsToBase(t *testing.T) {
	rng := rand.New(rand.NewPCG(731, 2))
	const f = 0.1
	for iter := 0; iter < 200; iter++ {
		n := 2 + rng.IntN(3)
		families := make(map[string][]float64, n)
		for i := 0; i < n; i++ {
			series := make([]float64, 14+rng.IntN(20))
			for j := range series {
				series[j] = 50 + rng.Float64()*20
			}
			families[string(rune('a'+i))] = series
		}
		flat := ptr(1e9)
		if iter%2 == 1 {
			flat = ptr(rng.Float64() * 300)
		}
		base, shares, err := familyStableSplit(families, flat, f)
		require.NoError(t, err)
		require.NotNil(t, base)
		require.Len(t, shares, n)
		sum := 0.0
		for name, s := range shares {
			assert.LessOrEqual(t, s, *base+1e-12, "family %s share exceeds the base", name)
			sum += s
		}
		assert.InDelta(t, *base, sum, 1e-9*max(1, *base), "shares must sum to the base")
	}
}

// TestFamilySplit_NoMinimumGetsZeroShare: a volatile family and a short family
// both fail the stable gate, so both get a zero share (key present) and the
// steady families split the base alone.
func TestFamilySplit_NoMinimumGetsZeroShare(t *testing.T) {
	const f = 0.1
	volatile := append([]float64{1, 1, 1, 1, 1}, steadySeries(100, 9)...)
	families := map[string][]float64{
		"m5": steadySeries(10, 14),
		"c5": steadySeries(30, 14),
		"x1": volatile,
		"s9": steadySeries(5, 13),
	}
	base, shares, err := familyStableSplit(families, ptr(1e9), f)
	require.NoError(t, err)
	require.NotNil(t, base)
	assert.InDelta(t, 36, *base, 1e-9)
	require.Contains(t, shares, "x1")
	require.Contains(t, shares, "s9")
	assert.Equal(t, 0.0, shares["x1"])
	assert.Equal(t, 0.0, shares["s9"])
	assert.InDelta(t, 9, shares["m5"], 1e-9)
	assert.InDelta(t, 27, shares["c5"], 1e-9)
}

// TestFamilySplit_SingleFamilyCarriesAll: one family carries the whole base,
// bounded by its own minimum; no family data or a nil flat leaves no base and
// no split, so the allocator never buys a family-less EC2 Instance SP.
func TestFamilySplit_SingleFamilyCarriesAll(t *testing.T) {
	base, shares, err := familyStableSplit(map[string][]float64{"m5": steadySeries(10, 14)}, ptr(7), 0.1)
	require.NoError(t, err)
	assert.Equal(t, 7.0, *base)
	assert.Equal(t, map[string]float64{"m5": 7.0}, shares)

	base, shares, err = familyStableSplit(map[string][]float64{"m5": steadySeries(10, 14)}, ptr(50), 0.1)
	require.NoError(t, err)
	assert.InDelta(t, 9.0, *base, 1e-9, "the family's own minimum bounds the base")
	assert.InDelta(t, 9.0, shares["m5"], 1e-9)

	base, shares, err = familyStableSplit(map[string][]float64{"m5": steadySeries(10, 13)}, ptr(7), 0.1)
	require.NoError(t, err)
	assert.Nil(t, base, "a single family that fails the gate leaves no base")
	assert.Nil(t, shares)

	base, shares, err = familyStableSplit(map[string][]float64{}, ptr(5), 0.1)
	require.NoError(t, err)
	assert.Nil(t, base)
	assert.Nil(t, shares)

	base, shares, err = familyStableSplit(map[string][]float64{"m5": steadySeries(10, 14), "c5": steadySeries(30, 14)}, nil, 0.1)
	require.NoError(t, err)
	assert.Nil(t, base)
	assert.Nil(t, shares)
}

// TestFamilySplit_ShareNeverExceedsBase: b*c/c rounds above b for these values,
// so without the clamp a family that carries the whole total exceeds the base.
func TestFamilySplit_ShareNeverExceedsBase(t *testing.T) {
	b, c := 3.7860681245451464, 46.45511343472187
	require.Greater(t, b*c/c, b, "fixture must expose the rounding")
	families := map[string][]float64{"m5": steadySeries(c, 14), "c5": steadySeries(5, 13)}
	base, shares, err := familyStableSplit(families, ptr(b), 0)
	require.NoError(t, err)
	require.NotNil(t, base)
	assert.LessOrEqual(t, shares["m5"], *base)
}
