package common //nolint:revive // Same domain package as the production Recommendation type.

import (
	"encoding/json"
	"math/big"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecommendationExactCoverageIsTransient(t *testing.T) {
	t.Parallel()
	percent := big.NewRat(160, 3)
	value, _ := percent.Float64()
	rec := Recommendation{ExistingCoveragePct: value, ExistingCoveragePercentExact: percent, Count: 4}
	without := rec
	without.ExistingCoveragePercentExact = nil
	payload, err := json.Marshal(rec)
	require.NoError(t, err)
	want, err := json.Marshal(without)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(payload))
	var decoded Recommendation
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.Nil(t, decoded.ExistingCoveragePercentExact)
	assert.Equal(t, rec.ExistingCoveragePct, decoded.ExistingCoveragePct)
	assert.Equal(t, rec.Count, decoded.Count)
	field, ok := reflect.TypeFor[Recommendation]().FieldByName("ExistingCoveragePercentExact")
	require.True(t, ok)
	assert.Equal(t, "-", field.Tag.Get("csv"))
}

func TestScaleRecommendationCostsPreservesImmutableCoverage(t *testing.T) {
	t.Parallel()
	percent := big.NewRat(160, 3)
	rec := Recommendation{ExistingCoveragePercentExact: percent, CommitmentCost: 100}
	copyRec := rec
	scaled := ScaleRecommendationCosts(copyRec, 2)
	assert.Same(t, percent, scaled.ExistingCoveragePercentExact)
	assert.Equal(t, big.NewRat(160, 3), percent)
	assert.Equal(t, 100.0, rec.CommitmentCost)
	assert.Equal(t, 200.0, scaled.CommitmentCost)
}
