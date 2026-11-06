package managedredis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// Engine filters and the CSV Engine column read CacheDetails, so the Redis
// recommendation must carry Engine and NodeType. Shards stays 0 (unknown).
func TestConvertRecommendation_CacheDetails(t *testing.T) {
	t.Parallel()
	c := NewClient(nil, "sub-abc", "westeurope")
	azRec := mocks.BuildLegacyReservationRecommendation(
		mocks.WithRegion("westeurope"),
		mocks.WithNormalizedSize("Premium_P3"),
		mocks.WithQuantity(1),
		mocks.WithCosts(1000.0, 700.0, 300.0),
	)
	rec := c.convertRecommendation(context.Background(), azRec)
	require.NotNil(t, rec)

	details, ok := rec.Details.(*common.CacheDetails)
	require.True(t, ok, "Details must be a *common.CacheDetails pointer")
	assert.Equal(t, "redis", details.Engine)
	assert.Equal(t, "Premium_P3", details.NodeType)
	assert.Equal(t, rec.ResourceType, details.NodeType)
	assert.Zero(t, details.Shards, "shard count is unknown")

	assert.Equal(t, "redis", common.EngineFromDetails(rec.Details))
	assert.True(t, recfilter.Filters{IncludeEngines: []string{"redis"}}.IncludesEngine(rec))
	assert.False(t, recfilter.Filters{IncludeEngines: []string{"valkey"}}.IncludesEngine(rec))
}
