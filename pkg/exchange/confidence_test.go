package exchange

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfidenceThresholdValues(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 200.0, ConfidenceHighSavingsUSD, 0)
	assert.Equal(t, 3, ConfidenceHighMinCount)
	assert.InDelta(t, 50.0, ConfidenceMediumSavingsUSD, 0)
}

func TestConfidenceComponentBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		savings *float64
		count   int
		want    float64
	}{
		{"high at both thresholds", floatPtr(ConfidenceHighSavingsUSD), ConfidenceHighMinCount, 1.0},
		{"just below high savings", floatPtr(ConfidenceHighSavingsUSD - 0.01), ConfidenceHighMinCount, 0.5},
		{"just below high count", floatPtr(ConfidenceHighSavingsUSD), ConfidenceHighMinCount - 1, 0.5},
		{"medium at threshold", floatPtr(ConfidenceMediumSavingsUSD), 1, 0.5},
		{"just below medium", floatPtr(ConfidenceMediumSavingsUSD - 0.01), 1, 0.0},
		{"count zero is neutral even when low savings", floatPtr(1), 0, 0.5},
		{"count zero is neutral even when high savings", floatPtr(ConfidenceHighSavingsUSD * 10), 0, 0.5},
		{"nil savings is neutral", nil, ConfidenceHighMinCount, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := confidenceComponent(OfferingOption{SavingsAbs: tt.savings, RecommendationCount: tt.count})
			assert.InDelta(t, tt.want, got, 0)
		})
	}
}
