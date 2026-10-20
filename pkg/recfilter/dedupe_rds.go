package recfilter

import (
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// rdsSizeUnits is the RDS normalized-unit factor per instance size, scaled by 4 so
// nano (0.25) stays an integer:
// https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_WorkingWithReservedDBInstances.html#USER_WorkingWithReservedDBInstances.SizeFlexible
var rdsSizeUnits = map[string]int{
	"nano": 1, "micro": 2, "small": 4, "medium": 8, "large": 16, "xlarge": 32,
	"2xlarge": 64, "4xlarge": 128, "6xlarge": 192, "8xlarge": 256, "10xlarge": 320,
	"12xlarge": 384, "16xlarge": 512, "24xlarge": 768, "32xlarge": 1024,
}

// rdsFlexKey reports the instance family and size units of an AWS RDS instance class
// such as db.r6g.large. An RDS reservation pays for any size of the same family,
// engine, region and deployment, so the family replaces the exact class in the dedupe
// key and the budget is counted in units. Anything unrecognized returns false and
// keeps the exact-class key, which only ever matches the same class.
func rdsFlexKey(providerType common.ProviderType, service common.ServiceType, resourceType string) (family string, units int, ok bool) {
	if providerType != common.ProviderAWS || (service != common.ServiceRDS && service != common.ServiceRelationalDB) {
		return "", 0, false
	}
	i := strings.LastIndex(resourceType, ".")
	if i <= 0 {
		return "", 0, false
	}
	units, ok = rdsSizeUnits[strings.ToLower(resourceType[i+1:])]
	if !ok {
		return "", 0, false
	}
	return strings.ToLower(resourceType[:i]) + ".*", units, true
}
