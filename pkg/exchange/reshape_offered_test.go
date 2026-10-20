package exchange

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func analyzeOne(instanceType string, count int32, util float64, opts ...ReshapeOption) []ReshapeRecommendation {
	ris := []RIInfo{{ID: "ri-1", InstanceType: instanceType, InstanceCount: count, OfferingClass: "convertible"}}
	return AnalyzeReshaping(ris, []UtilizationInfo{{RIID: "ri-1", UtilizationPercent: util}}, 95, opts...)
}

func TestAnalyzeReshaping_OfferedCatalogRestrictsTargets(t *testing.T) {
	t.Parallel()
	m5NoSix := []string{"m5.large", "m5.xlarge", "m5.2xlarge", "m5.4xlarge", "m5.8xlarge", "m5.12xlarge"}

	// Without a catalog the best fit is the unoffered m5.6xlarge.
	recs := analyzeOne("m5.12xlarge", 1, 50)
	require.Len(t, recs, 1)
	assert.Equal(t, "m5.6xlarge", recs[0].TargetInstanceType)

	recs = analyzeOne("m5.12xlarge", 1, 50, WithOfferedInstanceTypes(m5NoSix))
	require.Len(t, recs, 1)
	assert.Equal(t, "m5.4xlarge", recs[0].TargetInstanceType)
	assert.Equal(t, int32(2), recs[0].TargetCount)
}

func TestAnalyzeReshaping_OfferedCatalogCanExceedLegacyLadder(t *testing.T) {
	t.Parallel()
	t3 := []string{"t3.nano", "t3.micro", "t3.small", "t3.medium", "t3.large", "t3.xlarge", "t3.2xlarge"}
	recs := analyzeOne("t3.2xlarge", 100, 90, WithOfferedInstanceTypes(t3))
	require.Len(t, recs, 1)
	assert.Equal(t, "t3.2xlarge", recs[0].TargetInstanceType)
	assert.Equal(t, int32(90), recs[0].TargetCount)

	// A catalog that offers 32xlarge lets a big fleet target it.
	big := []string{"m6i.16xlarge", "m6i.24xlarge", "m6i.32xlarge"}
	recs = analyzeOne("m6i.32xlarge", 4, 70, WithOfferedInstanceTypes(big))
	require.Len(t, recs, 1)
	assert.Equal(t, "m6i.32xlarge", recs[0].TargetInstanceType)
	assert.Equal(t, int32(3), recs[0].TargetCount)
}

func TestAnalyzeReshaping_FamilyMissingFromCatalogHasReason(t *testing.T) {
	t.Parallel()
	recs := analyzeOne("m5.xlarge", 1, 50, WithOfferedInstanceTypes([]string{"c5.large", "c5.xlarge"}))
	require.Len(t, recs, 1)
	assert.Empty(t, recs[0].TargetInstanceType)
	assert.Contains(t, recs[0].Reason, "instance family not offered in region")

	// A catalog holding only metal still counts as a catalog.
	recs = analyzeOne("m5.xlarge", 1, 50, WithOfferedInstanceTypes([]string{"m5.metal"}))
	require.Len(t, recs, 1)
	assert.Contains(t, recs[0].Reason, "instance family not offered in region")
}

func TestAnalyzeReshaping_EmptyCatalogKeepsLegacyLadder(t *testing.T) {
	t.Parallel()
	want := analyzeOne("m5.12xlarge", 1, 50)
	for _, cat := range [][]string{nil, {}} {
		assert.Equal(t, want, analyzeOne("m5.12xlarge", 1, 50, WithOfferedInstanceTypes(cat)))
	}
}

func TestAnalyzeReshaping_FamilyOfferedButNoSizeFits(t *testing.T) {
	t.Parallel()
	// Only the source size is offered: nothing smaller to move to, so no rec.
	recs := analyzeOne("m5.xlarge", 1, 50, WithOfferedInstanceTypes([]string{"m5.xlarge"}))
	assert.Empty(t, recs)
}

func TestAnalyzeReshaping_MetalIgnoredInCatalog(t *testing.T) {
	t.Parallel()
	cat := []string{"m5.large", "m5.xlarge", "m5.metal"}
	recs := analyzeOne("m5.xlarge", 1, 50, WithOfferedInstanceTypes(cat))
	require.Len(t, recs, 1)
	assert.Equal(t, "m5.large", recs[0].TargetInstanceType)
}

func TestAnalyzeReshapingWithRecs_PassesOfferedOption(t *testing.T) {
	t.Parallel()
	ris := []RIInfo{{ID: "ri-1", InstanceType: "m5.12xlarge", InstanceCount: 1, OfferingClass: "convertible"}}
	utils := []UtilizationInfo{{RIID: "ri-1", UtilizationPercent: 50}}
	recs := AnalyzeReshapingWithRecs(t.Context(), ris, utils, 95, "us-east-1", "USD", nil,
		WithOfferedInstanceTypes([]string{"m5.4xlarge", "m5.12xlarge"}))
	require.Len(t, recs, 1)
	assert.Equal(t, "m5.4xlarge", recs[0].TargetInstanceType)
}
