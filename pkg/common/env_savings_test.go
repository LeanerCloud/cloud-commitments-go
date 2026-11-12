package common

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPositiveIntFromEnv(t *testing.T) {
	const name = "CUDLY_TEST_POSITIVE_INT"
	for _, tc := range []struct {
		env  string
		want int
	}{{"", 7}, {"50", 50}, {"many", 7}, {"0", 7}, {"-3", 7}} {
		t.Setenv(name, tc.env)
		assert.Equal(t, tc.want, PositiveIntFromEnv(name, 7), "env=%q", tc.env)
	}
}

func TestSavingsPct(t *testing.T) {
	assert.InDelta(t, 16, SavingsPct(100, 84), 1e-9)
	assert.InDelta(t, -50, SavingsPct(100, 150), 1e-9)
	assert.InDelta(t, 100, SavingsPct(100, 0), 1e-9)
	for _, od := range []float64{0, -1} {
		got := SavingsPct(od, 0)
		assert.Equal(t, 0.0, got)
		assert.False(t, math.IsNaN(got) || math.IsInf(got, 0))
	}
}

func TestShouldIncludeService(t *testing.T) {
	assert.True(t, ShouldIncludeService(RecommendationParams{}, ServiceCompute))
	assert.True(t, ShouldIncludeService(RecommendationParams{Service: ServiceCompute}, ServiceCompute))
	assert.False(t, ShouldIncludeService(RecommendationParams{Service: ServiceCache}, ServiceCompute))
}
