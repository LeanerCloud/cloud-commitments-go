package ec2

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// offeringWant is what an exchange lookup asked AWS for. The typed request
// fields are hints; offeringMismatch re-checks the offerings AWS returns
// before an id is handed to an exchange. A zero-valued field is unchecked.
type offeringWant struct {
	instanceType string
	productDesc  string
	tenancy      types.Tenancy
	scope        types.Scope
	duration     int64
	class        types.OfferingClassType
	offeringType types.OfferingTypeValues
	az           string
}

// want maps the lookup query onto the fields offeringMismatch re-checks.
func (q ec2OfferingQuery) want() offeringWant {
	return offeringWant{
		instanceType: string(q.instanceType),
		productDesc:  string(q.productDesc),
		tenancy:      q.tenancy,
		scope:        q.scope,
		duration:     q.duration,
		class:        q.offeringClass,
		offeringType: q.wantOfferingType,
		az:           q.availabilityZone,
	}
}

const vpcProductSuffix = " (Amazon VPC)"

// offeringChecks are tried in order; the first failing one names the reason.
// Class, tenancy, duration and offering type are strict (an unset offering
// field is a mismatch); instance type, scope and product are compared only
// when AWS populated the offering field.
var offeringChecks = []struct {
	reason   string
	mismatch func(o types.ReservedInstancesOffering, w offeringWant) bool
}{
	{"zone", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return !offeringInZone(o, w.az)
	}},
	{"class", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return w.class != "" && o.OfferingClass != w.class
	}},
	{"tenancy", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return w.tenancy != "" && o.InstanceTenancy != w.tenancy
	}},
	{"duration", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return w.duration != 0 && aws.ToInt64(o.Duration) != w.duration
	}},
	{"offering-type", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return w.offeringType != "" && o.OfferingType != w.offeringType
	}},
	{"instance-type", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return w.instanceType != "" && o.InstanceType != "" && string(o.InstanceType) != w.instanceType
	}},
	{"scope", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		return w.scope != "" && o.Scope != "" && o.Scope != w.scope
	}},
	{"product", func(o types.ReservedInstancesOffering, w offeringWant) bool {
		got, want := string(o.ProductDescription), w.productDesc
		if got == "" || want == "" {
			return false
		}
		return strings.TrimSuffix(got, vpcProductSuffix) != strings.TrimSuffix(want, vpcProductSuffix)
	}},
}

// offeringMismatch returns the reason o does not satisfy w, or "" on a match.
func offeringMismatch(o types.ReservedInstancesOffering, w offeringWant) string {
	for _, c := range offeringChecks {
		if c.mismatch(o, w) {
			return c.reason
		}
	}
	return ""
}

// offeringSkips counts skipped offerings by mismatch reason, so a systematic
// omission by AWS does not read as "not found".
type offeringSkips map[string]int

func (s offeringSkips) total() int {
	n := 0
	for _, c := range s {
		n += c
	}
	return n
}

// String renders "class=1, tenancy=2" with reasons sorted.
func (s offeringSkips) String() string {
	parts := make([]string, 0, len(s))
	for r, c := range s {
		parts = append(parts, fmt.Sprintf("%s=%d", r, c))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// note is the error-message suffix, empty when nothing was skipped.
func (s offeringSkips) note() string {
	if s.total() == 0 {
		return ""
	}
	return fmt.Sprintf("; %d offerings skipped: %s", s.total(), s)
}
