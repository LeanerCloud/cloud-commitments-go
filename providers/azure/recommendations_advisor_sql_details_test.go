package azure

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

// Issue #735: the dedupe key includes the engine, and held SQL Database
// commitments carry "sqlserver", so an Advisor SQL row without Details never
// matched them.

func advisorSQLRow(t *testing.T, over map[string]*string) common.Recommendation {
	t.Helper()
	ext := reservationExt(map[string]*string{"reservedResourceType": strPtr("SqlDatabases"), "sku": strPtr("GP_Gen5_2")})
	for k, v := range over {
		ext[k] = v
	}
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	rec := a.convertAdvisorRecommendation(context.Background(), advisorRec(subscriptionScope, ext))
	require.NotNil(t, rec)
	return *rec
}

type heldCommitmentsClient struct {
	provider.ServiceClient
	held []common.Commitment
}

func (c heldCommitmentsClient) GetExistingCommitments(context.Context) ([]common.Commitment, error) {
	return c.held, nil
}

func TestAdvisorSQLRowCarriesSQLServerDetails(t *testing.T) {
	rec := advisorSQLRow(t, nil)
	require.Equal(t, common.ServiceRelationalDB, rec.Service)
	assert.Equal(t, &common.DatabaseDetails{Engine: "sqlserver", InstanceClass: "GP_Gen5_2"}, rec.Details)
}

func TestAdvisorSQLRowIsDedupedAgainstHeldSQLCommitment(t *testing.T) {
	rec := advisorSQLRow(t, nil)
	held := heldCommitmentsClient{held: []common.Commitment{{
		Provider: common.ProviderAzure, Service: common.ServiceRelationalDB, Region: rec.Region,
		ResourceType: rec.ResourceType, Engine: "sqlserver", State: common.CommitmentStateActive,
		StartDate: time.Now().Add(-time.Hour), Count: rec.Count,
	}}}

	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, held)

	require.NoError(t, err)
	assert.Empty(t, passed, "a held reservation covering the whole count removes the Advisor row")
	assert.Len(t, filtered, 1)
}

func TestAdvisorDetailsOnlyForReservationsOfTheirService(t *testing.T) {
	assert.Nil(t, advisorSQLRow(t, map[string]*string{"reservedResourceType": strPtr("VirtualMachines")}).Details, "compute row stays without details")

	sp := advisorSQLRow(t, map[string]*string{"recommendationSubCategory": strPtr("SavingsPlan")})
	assert.Equal(t, common.CommitmentSavingsPlan, sp.CommitmentType)
	assert.Nil(t, sp.Details, "a Savings Plan row is not given SQL details")

	redis := redisAdvisorRow(redisAdapter(t, realRedisClient(), nil), "westus")
	assert.Equal(t, "redis", common.EngineFromDetails(redis.Details))
}

func TestAttachAdvisorDetailsKeepsExistingDetails(t *testing.T) {
	existing := &common.DatabaseDetails{Engine: "sqlserver", InstanceClass: "keep", AZConfig: "zoneRedundant"}
	rec := &common.Recommendation{Service: common.ServiceRelationalDB, CommitmentType: common.CommitmentReservedInstance, ResourceType: "GP_Gen5_2", Details: existing}

	attachAdvisorDetails(rec)

	assert.Same(t, existing, rec.Details)
}
