package recfilter

import (
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// ec2FlexKey reports the family key and the quarter normalized units of one node for an
// EC2 instance type whose platform and tenancy are exactly Linux/Unix and default. A
// regional reservation of that kind pays for any size of the same family in its region
// (https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/apply_ri.html), so the family
// replaces the exact type in the key and the budget is counted in units (quarters of a
// normalized unit, so nano stays an integer). Anything else returns false and keeps the
// exact-type key: other platforms and dedicated tenancy are not size-flexible, and an
// empty (wildcard) platform or tenancy may not be, so it must never join or spend a
// flex budget. Callers also require a regional scope for entries: zonal
// reservations are not size-flexible. Sizes without a known factor, such as metal, stay exact.
func ec2FlexKey(resourceType, region, platform, tenancy string) (family string, units int, ok bool) {
	if platform != "linux/unix" || tenancy != "default" {
		return "", 0, false
	}
	prefix, size, found := strings.Cut(resourceType, ".")
	nf := common.EC2NormalizationFactor(size)
	if !found || prefix == "" || nf == 0 {
		return "", 0, false
	}
	return fmt.Sprintf("ec2flex|%s|%s", strings.ToLower(prefix), region), int(nf * 4), true
}

// consumeFamily spends the size-flexible budget of the recommendation's family on what is
// still uncovered and returns the count left. The budget is the unspent units of every regional
// Linux/Unix default-tenancy entry of the family, spent in input order. The covered nodes
// round down and the spent units are exact, so a fractional remainder is never credited.
func (d *ec2Dedupe) consumeFamily(rec common.Recommendation, platform, tenancy string, remaining int) int {
	family, recUnits, ok := ec2FlexKey(rec.ResourceType, rec.Region, platform, tenancy)
	if !ok || remaining <= 0 {
		return remaining
	}
	entries := d.families[family]
	budget := 0
	for _, e := range entries {
		budget += e.flexLeft
	}
	covered := min(budget/recUnits, remaining)
	need := covered * recUnits
	for _, e := range entries {
		if need <= 0 {
			break
		}
		take := min(e.flexLeft, need)
		e.flexLeft -= take
		e.count = (e.flexLeft + e.flexUnits - 1) / e.flexUnits
		need -= take
	}
	return remaining - covered
}
