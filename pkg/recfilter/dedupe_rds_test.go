package recfilter

import (
	"cmp"
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRDSSizeFlexDedupe(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		commitment string
		deployment string
		count      int
		service    common.ServiceType
		rec        string
		recEngine  string // empty: same as the commitment
		recDeploy  string
		recRegion  string
		recCount   int
		want       int
		engine     string // empty: mysql
	}{
		{"large covers two mediums", "db.r6g.large", "single-az", 1, common.ServiceRelationalDB, "db.r6g.medium", "mysql", "single-az", "us-east-1", 2, 0, ""},
		{"large covers one of three mediums", "db.r6g.large", "single-az", 1, common.ServiceRDS, "db.r6g.medium", "mysql", "single-az", "us-east-1", 3, 1, ""},
		{"two mediums cover half a large: no credit", "db.r6g.medium", "single-az", 1, common.ServiceRDS, "db.r6g.large", "mysql", "single-az", "us-east-1", 1, 1, ""},
		{"two mediums cover a large", "db.r6g.medium", "single-az", 2, common.ServiceRDS, "db.r6g.large", "mysql", "single-az", "us-east-1", 1, 0, ""},
		{"xlarge covers larger count of large", "db.r6g.xlarge", "single-az", 2, common.ServiceRDS, "db.r6g.large", "mysql", "single-az", "us-east-1", 4, 0, ""},
		{"other family", "db.r6g.large", "single-az", 1, common.ServiceRDS, "db.m6g.medium", "mysql", "single-az", "us-east-1", 1, 1, ""},
		{"other engine", "db.r6g.large", "single-az", 1, common.ServiceRDS, "db.r6g.medium", "postgres", "single-az", "us-east-1", 1, 1, ""},
		{"other region", "db.r6g.large", "single-az", 1, common.ServiceRDS, "db.r6g.medium", "mysql", "single-az", "us-west-2", 1, 1, ""},
		{"other deployment", "db.r6g.large", "single-az", 1, common.ServiceRDS, "db.r6g.medium", "mysql", "multi-az", "us-east-1", 1, 1, ""},
		{"unknown size stays exact", "db.r6g.metal", "single-az", 1, common.ServiceRDS, "db.r6g.medium", "mysql", "single-az", "us-east-1", 1, 1, ""},
		{"exact class still matches", "db.r6g.metal", "single-az", 1, common.ServiceRDS, "db.r6g.metal", "mysql", "single-az", "us-east-1", 1, 0, ""},
		{"postgres family flex", "db.r6g.large", "single-az", 1, common.ServiceRDS, "db.r6g.medium", "", "single-az", "us-east-1", 2, 0, "postgres"},
		{"aurora mysql flex", "db.r6g.large", "", 1, common.ServiceRDS, "db.r6g.medium", "", "", "us-east-1", 2, 0, "Aurora MySQL"},
		{"sqlserver license included exact", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.medium", "", "single-az", "us-east-1", 2, 2, "sqlserver-se (li)"},
		{"sqlserver exact", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.medium", "", "single-az", "us-east-1", 2, 2, "sqlserver-se"},
		{"oracle license included exact", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.medium", "", "single-az", "us-east-1", 2, 2, "oracle-se2 (li)"},
		{"oracle unmarked exact", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.medium", "", "single-az", "us-east-1", 2, 2, "oracle-se2"},
		{"db2 exact", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.medium", "", "single-az", "us-east-1", 2, 2, "db2-se"},
		{"unknown engine exact", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.medium", "", "single-az", "us-east-1", 2, 2, "somedb"},
		{"sqlserver same class still dedupes", "db.m6i.large", "single-az", 1, common.ServiceRDS, "db.m6i.large", "", "single-az", "us-east-1", 1, 0, "sqlserver-se"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			commitEngine := cmp.Or(tt.engine, "mysql")
			recEngine := cmp.Or(tt.recEngine, commitEngine)
			client := &fakeServiceClient{commitments: []common.Commitment{{
				Provider: common.ProviderAWS, Service: tt.service, ResourceType: tt.commitment,
				Region: "us-east-1", Engine: commitEngine, Deployment: tt.deployment, Count: tt.count,
				State: common.CommitmentStateActive, StartDate: time.Now(),
			}}}
			rec := common.Recommendation{
				Provider: common.ProviderAWS, Service: common.ServiceRDS, ResourceType: tt.rec, Region: tt.recRegion, Count: tt.recCount,
				Details: &common.DatabaseDetails{Engine: recEngine, AZConfig: tt.recDeploy},
			}
			passed, filtered, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)
			require.NoError(t, err)
			if tt.want == 0 {
				assert.Empty(t, passed)
				assert.Len(t, filtered, 1)
				return
			}
			require.Len(t, passed, 1)
			assert.Equal(t, tt.want, passed[0].Count)
		})
	}
}

func TestRDSSizeFlexBudgetSharedAcrossRecs(t *testing.T) {
	t.Parallel()
	client := &fakeServiceClient{commitments: []common.Commitment{{
		Provider: common.ProviderAWS, Service: common.ServiceRDS, ResourceType: "db.r6g.xlarge",
		Region: "us-east-1", Engine: "mysql", Count: 1, State: common.CommitmentStateActive, StartDate: time.Now(),
	}}}
	mk := func(size string, n int) common.Recommendation {
		return common.Recommendation{Provider: common.ProviderAWS, Service: common.ServiceRDS, ResourceType: size,
			Region: "us-east-1", Count: n, Details: &common.DatabaseDetails{Engine: "mysql"}}
	}
	// 8 units of xlarge: two larges (4+4) use all of it, the third large is bought.
	passed, _, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(),
		[]common.Recommendation{mk("db.r6g.large", 1), mk("db.r6g.large", 2)}, client)
	require.NoError(t, err)
	require.Len(t, passed, 1)
	assert.Equal(t, 1, passed[0].Count)
}

// An Oracle BYOL reservation keeps the exact-class key on both sides: it still suppresses
// a recommendation of the same class (as before size-flex), and covers no other size.
func TestRDSOracleBYOLStaysExactClass(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, commitEngine, recEngine, recSize string
		want                                   int
	}{
		{"same class byol string", "oracle-se2 (byol)", "oracle-se2 (byol)", "db.m5.large", 0},
		{"same class normalized", "oracle-se2", "oracle", "db.m5.large", 0},
		{"byol does not cover other size", "oracle-se2 (byol)", "oracle-se2 (byol)", "db.m5.medium", 1},
		{"normalized does not cover other size", "oracle-se2", "oracle", "db.m5.medium", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeServiceClient{commitments: []common.Commitment{{
				Provider: common.ProviderAWS, Service: common.ServiceRelationalDB, ResourceType: "db.m5.large",
				Region: "us-east-1", Engine: tt.commitEngine, Deployment: "single-az", Count: 1,
				State: common.CommitmentStateActive, StartDate: time.Now(),
			}}}
			rec := common.Recommendation{
				Provider: common.ProviderAWS, Service: common.ServiceRDS, ResourceType: tt.recSize, Region: "us-east-1", Count: 1,
				Details: &common.DatabaseDetails{Engine: tt.recEngine, AZConfig: "single-az"},
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
