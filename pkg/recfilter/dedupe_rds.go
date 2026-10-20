package recfilter

import (
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// rdsFlexKey reports the instance family key and size units of an AWS RDS instance class
// such as db.r6g.large for a size-flexible engine. Such a reservation pays for any size
// of the same family, engine, region and deployment, so the family replaces the exact
// class in the dedupe key and the budget is counted in units (quarters of a normalized
// unit, so nano stays an integer). Anything else returns false and keeps the
// exact-class key, which only ever matches the same class.
func rdsFlexKey(providerType common.ProviderType, service common.ServiceType, engine, resourceType string) (family string, units int, ok bool) {
	if providerType != common.ProviderAWS || (service != common.ServiceRDS && service != common.ServiceRelationalDB) || !common.RDSSizeFlexibleEngine(engine) {
		return "", 0, false
	}
	nu := common.RDSInstanceNU(resourceType)
	if nu == 0 {
		return "", 0, false
	}
	return strings.ToLower(common.RDSFamily(resourceType)) + ".*", int(nu * 4), true
}

// Normalized deployment names (common.NormalizeDeploymentName) of the RDS deployment options.
const (
	rdsSingleAZ = "singleaz"
	rdsMultiAZ  = "multiaz"
)

// rdsDeploymentFactor is how many times the size units a node of the deployment option
// counts in a size-flexible budget: a Multi-AZ node runs two instances, so it counts
// double: https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_WorkingWithReservedDBInstances.html#USER_WorkingWithReservedDBInstances.SizeFlexible
func rdsDeploymentFactor(normalizedDeployment string) int {
	if normalizedDeployment == rdsMultiAZ {
		return 2
	}
	return 1
}
