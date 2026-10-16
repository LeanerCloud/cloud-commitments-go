package recommendations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// poolKeyShapes holds, per service, the ResourceType the real producers emit
// for the same underlying size. The non-RDS pool key is region:type with no
// service dimension, so these shapes must stay disjoint (see commitmentPoolKey).
//
//	EC2         services/ec2/client.go:111          string(ri.InstanceType)      m5.large
//	ElastiCache services/elasticache/client.go:100  aws.ToString(CacheNodeType)  cache.m5.large
//	OpenSearch  services/opensearch/client.go:115   string(ri.InstanceType)      m5.large.search
//	Redshift    services/redshift/client.go:119     aws.ToString(NodeType)       ra3.xlplus
//	MemoryDB    services/memorydb/client.go:95      aws.ToString(NodeType)       db.r6g.large
//	RDS         own 4-part rdsPoolKey (coverage.go) db.r6g.large + engine + deployment
var poolKeyShapes = []struct {
	name         string
	service      common.ServiceType
	resourceType string
	engine       string
	deployment   string
}{
	{"ec2", common.ServiceEC2, "m5.large", "", ""},
	{"elasticache", common.ServiceElastiCache, "cache.m5.large", "", ""},
	{"opensearch", common.ServiceOpenSearch, "m5.large.search", "", ""},
	{"redshift", common.ServiceRedshift, "ra3.xlplus", "", ""},
	{"memorydb", common.ServiceMemoryDB, "db.r6g.large", "", ""},
	{"rds", common.ServiceRDS, "db.r6g.large", "mysql", "single-az"},
}

func shapeCommitment(region string, i int) common.Commitment {
	s := poolKeyShapes[i]
	return common.Commitment{
		Service: s.service, Region: region, ResourceType: s.resourceType,
		Engine: s.engine, Deployment: s.deployment,
	}
}

func shapeRec(region string, i int) common.Recommendation {
	s := poolKeyShapes[i]
	rec := common.Recommendation{Service: s.service, Region: region, ResourceType: s.resourceType}
	if s.engine != "" {
		rec.Details = &common.DatabaseDetails{Engine: s.engine, AZConfig: s.deployment}
	}
	return rec
}

func TestPoolKeys_DisjointAcrossServices(t *testing.T) {
	const region = "us-east-1"
	seen := map[string]string{}
	for i, s := range poolKeyShapes {
		key := commitmentPoolKey(shapeCommitment(region, i))
		if prev, dup := seen[key]; dup {
			t.Errorf("commitment key %q collides: %s and %s", key, prev, s.name)
		}
		seen[key] = s.name
	}
	assert.Len(t, seen, len(poolKeyShapes))
}

func TestPoolKeys_CommitmentCoverageAndLookupAgree(t *testing.T) {
	const region = "eu-west-1"
	lookups := map[string]string{}
	for i, s := range poolKeyShapes {
		ck := commitmentPoolKey(shapeCommitment(region, i))
		lk := lookupPoolKey(shapeRec(region, i))
		assert.Equal(t, ck, lk, "%s: commitment and lookup keys must agree", s.name)
		if s.engine == "" {
			assert.Equal(t, ck, poolKey(region, s.resourceType),
				"%s: commitment and coverage keys must agree", s.name)
		}
		if prev, dup := lookups[lk]; dup {
			t.Errorf("lookup key %q collides: %s and %s", lk, prev, s.name)
		}
		lookups[lk] = s.name
	}
}
