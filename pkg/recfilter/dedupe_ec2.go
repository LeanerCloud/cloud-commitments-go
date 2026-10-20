package recfilter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

type scopeKind int

const (
	scopeUnknown scopeKind = iota
	scopeRegionalKind
	scopeZonalKind
)

// scopeState is the normalized scope of an EC2 commitment or recommendation.
type scopeState struct {
	kind scopeKind
	az   string
}

func newScopeState(rawScope, az string) scopeState {
	az = strings.ToLower(strings.TrimSpace(az))
	scope := common.NormalizeScopeName(rawScope)
	switch {
	case az != "" || scope == common.ScopeZonal:
		return scopeState{kind: scopeZonalKind, az: az}
	case scope == common.ScopeRegional:
		return scopeState{kind: scopeRegionalKind}
	default:
		return scopeState{}
	}
}

// scopeCovers reports whether an entry of the given scope can pay for a
// recommendation of the given scope. A regional reservation covers any zone's
// usage but a zonal one does not cover regional demand, so the rule is
// asymmetric. Unknown scope on either side is a wildcard.
func scopeCovers(entry, rec scopeState) bool {
	switch {
	case entry.kind == scopeUnknown || rec.kind == scopeUnknown:
		return true
	case entry.kind == scopeRegionalKind:
		return true
	case rec.kind != scopeZonalKind:
		return false
	default:
		return entry.az == "" || rec.az == "" || entry.az == rec.az
	}
}

type ec2Entry struct {
	platform, tenancy string
	scope             scopeState
	count, wildcards  int
	// flexUnits is the quarter normalized units of one node when the entry is a
	// regional Linux/Unix default-tenancy reservation of a known size, which pays
	// for any size of its family (see ec2FlexUnits); 0 keeps the entry exact-type.
	flexUnits int
	// flexLeft is the unspent budget in the same units. count stays the node count
	// the exact-type view sees: flexLeft rounded up to whole nodes.
	flexLeft int
}

func (e *ec2Entry) covers(platform, tenancy string, scope scopeState) bool {
	return (e.platform == "" || platform == "" || e.platform == platform) &&
		(e.tenancy == "" || tenancy == "" || e.tenancy == tenancy) &&
		scopeCovers(e.scope, scope)
}

// ec2Dedupe holds the AWS EC2 commitments by region and instance type. Account
// is deliberately not part of the identity: a commitment in another account
// still suppresses, as for every other service.
type ec2Dedupe struct {
	entries map[string][]*ec2Entry
	// families lists the size-flexible entries by family and region, in input order.
	families map[string][]*ec2Entry
	warned   map[string]bool
	logf     Logf
}

func isAWSCompute(p common.ProviderType, s common.ServiceType) bool {
	return p == common.ProviderAWS && (s == common.ServiceEC2 || s == common.ServiceCompute)
}

func ec2BaseKey(resourceType, region string) string {
	return fmt.Sprintf("ec2|%s|%s", resourceType, region)
}

func newEC2Dedupe(commitments []common.Commitment, logf Logf) *ec2Dedupe {
	d := &ec2Dedupe{entries: map[string][]*ec2Entry{}, families: map[string][]*ec2Entry{}, warned: map[string]bool{}, logf: logf}
	for i := range commitments {
		c := &commitments[i]
		if !isAWSCompute(c.Provider, c.Service) {
			continue
		}
		platform, tenancy, scope := d.normalize(c.Platform, c.Tenancy, c.Scope, c.AvailabilityZone)
		e := &ec2Entry{platform: platform, tenancy: tenancy, scope: scope, count: c.Count}
		e.wildcards = wildcardCount(e)
		key := ec2BaseKey(c.ResourceType, c.Region)
		d.entries[key] = append(d.entries[key], e)
		if family, units, ok := ec2FlexKey(c.ResourceType, c.Region, platform, tenancy); ok && scope.kind == scopeRegionalKind {
			e.flexUnits, e.flexLeft = units, max(e.count, 0)*units
			d.families[family] = append(d.families[family], e)
		}
	}
	for _, list := range d.entries {
		sort.SliceStable(list, func(i, j int) bool { return list[i].wildcards < list[j].wildcards })
	}
	return d
}

func wildcardCount(e *ec2Entry) int {
	n := 0
	for _, wild := range []bool{e.platform == "", e.tenancy == "", e.scope.kind == scopeUnknown,
		e.scope.kind == scopeZonalKind && e.scope.az == ""} {
		if wild {
			n++
		}
	}
	return n
}

func (d *ec2Dedupe) normalize(rawPlatform, rawTenancy, rawScope, az string) (platform, tenancy string, scope scopeState) {
	platform = common.NormalizePlatformName(rawPlatform)
	tenancy = common.NormalizeTenancyName(rawTenancy)
	d.warnIfUnrecognized("platform", rawPlatform, knownPlatform(platform))
	d.warnIfUnrecognized("tenancy", rawTenancy, tenancy)
	d.warnIfUnrecognized("scope", rawScope, common.NormalizeScopeName(rawScope))
	return platform, tenancy, newScopeState(rawScope, az)
}

// knownPlatform returns platform when it is an EC2 product description,
// else "". NormalizePlatformName never returns "" for non-empty input, so the
// unrecognized case is detected here.
func knownPlatform(platform string) string {
	if platform == "linux/unix" || strings.HasPrefix(platform, "windows") ||
		strings.HasPrefix(platform, "red hat") || strings.HasPrefix(platform, "suse") || strings.HasPrefix(platform, "linux with sql") {
		return platform
	}
	return ""
}

func (d *ec2Dedupe) warnIfUnrecognized(dim, raw, normalized string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || normalized != "" || d.warned[dim+"="+raw] {
		return
	}
	d.warned[dim+"="+raw] = true
	effect := "treating it as a wildcard"
	if dim == "platform" {
		effect = "matching it only against the same platform"
	}
	d.logf.printf("    [DuplicateChecker] WARNING: unrecognized EC2 %s %q; %s", dim, raw, effect)
}

// ec2DimsFromDetails reads the EC2 dimensions from ComputeDetails, held by
// value, by pointer, or as a typed nil.
func ec2DimsFromDetails(details common.ServiceDetails) (platform, tenancy, scope, az string) {
	switch d := details.(type) {
	case common.ComputeDetails:
		return d.Platform, d.Tenancy, d.Scope, d.AvailabilityZone
	case *common.ComputeDetails:
		if d != nil {
			return d.Platform, d.Tenancy, d.Scope, d.AvailabilityZone
		}
	}
	return "", "", "", ""
}

func (d *ec2Dedupe) handles(rec common.Recommendation) bool {
	return isAWSCompute(rec.Provider, rec.Service)
}

// adjust consumes matching entries, fewest wildcards first, and resizes the
// recommendation once to what is left. Entries are shared across
// recommendations, so which recommendation is suppressed depends on their
// order.
func (d *ec2Dedupe) adjust(rec common.Recommendation) (common.Recommendation, error) {
	rawPlatform, rawTenancy, rawScope, rawAZ := ec2DimsFromDetails(rec.Details)
	platform, tenancy, scope := d.normalize(rawPlatform, rawTenancy, rawScope, rawAZ)
	remaining := rec.Count
	for _, e := range d.entries[ec2BaseKey(rec.ResourceType, rec.Region)] {
		if remaining <= 0 {
			break
		}
		if e.count <= 0 || !e.covers(platform, tenancy, scope) {
			continue
		}
		used := min(e.count, remaining)
		e.count -= used
		e.flexLeft = max(e.flexLeft-used*e.flexUnits, 0)
		remaining -= used
	}
	remaining = d.consumeFamily(rec, platform, tenancy, remaining)
	if remaining <= 0 {
		d.logf.printf("    [DuplicateChecker] SKIP %s: recent commitments cover recommended %d", ec2BaseKey(rec.ResourceType, rec.Region), rec.Count)
		return common.Recommendation{Count: 0}, nil
	}
	return common.ResizeCount(rec, remaining)
}
