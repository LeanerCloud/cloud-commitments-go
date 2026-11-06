package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/managedredis"
)

// The dedupe key includes the engine, and held Redis reservations are keyed
// as "redis", so an Advisor-only Redis row must carry the same engine or a
// held reservation stops reducing it.
func TestAdvisorRedisRowCarriesRedisEngine(t *testing.T) {
	adv := redisAdvisorRow(redisAdapter(t, realRedisClient(), nil), "westus")
	require.Equal(t, common.ServiceMemoryDB, adv.Service)
	assert.Equal(t, "redis", common.EngineFromDetails(adv.Details))
	assert.Equal(t, managedredis.Details(adv.ResourceType), adv.Details)
}
