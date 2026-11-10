package recfilter

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure SQL Database reservations have no zone-redundancy dimension, so the held
// and recommended deployment slots must not split the pool (#715). Other
// providers and services keep the slot.
func TestDedupeDeploymentSlot(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		provider   common.ProviderType
		service    common.ServiceType
		heldDeploy string
		recDeploy  string
		want       int // recommended count that passes (recommended 1)
	}{
		{"azure sql rec empty vs held zoneRedundant", common.ProviderAzure, common.ServiceRelationalDB, "zoneRedundant", "", 0},
		{"azure sql rec none vs held zoneRedundant", common.ProviderAzure, common.ServiceRelationalDB, "zoneRedundant", "none", 0},
		{"azure sql rec zoneRedundant vs held none", common.ProviderAzure, common.ServiceRelationalDB, "none", "zoneRedundant", 0},
		{"azure sql both zoneRedundant", common.ProviderAzure, common.ServiceRelationalDB, "zoneRedundant", "zoneRedundant", 0},
		{"azure other service keeps slot", common.ProviderAzure, common.ServiceRDS, "zoneRedundant", "none", 1},
		{"azure other service same slot dedupes", common.ProviderAzure, common.ServiceRDS, "none", "none", 0},
		{"aws rds single vs multi stays separate", common.ProviderAWS, common.ServiceRDS, "multi-az", "single-az", 1},
		{"aws relational db single vs multi stays separate", common.ProviderAWS, common.ServiceRelationalDB, "multi-az", "single-az", 1},
		{"gcp relational db keeps slot", common.ProviderGCP, common.ServiceRelationalDB, "zoneRedundant", "none", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeServiceClient{commitments: []common.Commitment{{
				Provider: tt.provider, Service: tt.service, ResourceType: "GP_Gen5_2",
				Region: "eastus", Engine: "sqlserver", Deployment: tt.heldDeploy, Count: 1,
				State: common.CommitmentStateActive, StartDate: time.Now(),
			}}}
			rec := common.Recommendation{
				Provider: tt.provider, Service: tt.service, ResourceType: "GP_Gen5_2", Region: "eastus", Count: 1,
				Details: &common.DatabaseDetails{Engine: "sqlserver", AZConfig: tt.recDeploy},
			}
			passed, _, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)
			require.NoError(t, err)
			if tt.want == 0 {
				assert.Empty(t, passed)
				return
			}
			require.Len(t, passed, 1)
			assert.Equal(t, tt.want, passed[0].Count)
		})
	}
}
