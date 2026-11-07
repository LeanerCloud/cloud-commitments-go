package ec2

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

// fit is how one described reservation relates to the purchase being reconciled.
type fit int

const (
	fitNo        fit = iota // a field is present and different: not this purchase
	fitUncertain            // cannot be ruled in or out
	fitMatch                // every required field is present and equal
)

// worse combines two fits: a definite mismatch beats doubt, doubt beats a match.
func worse(a, b fit) fit {
	if a < b {
		return a
	}
	return b
}

func eq[T comparable](got, want, zero T) fit {
	switch got {
	case zero:
		return fitUncertain
	case want:
		return fitMatch
	}
	return fitNo
}

// riFit classifies one reserved instance against the rec's query, section 4.1 of the plan.
// tagged is true only for a match that carries the request's idempotency token.
func riFit(ri types.ReservedInstances, q ec2OfferingQuery, count int, token string, lo, hi time.Time) (f fit, tagged bool) {
	owned, other, dup := tagState(ri.Tags, token)
	if dup {
		return fitUncertain, false
	}
	if other {
		return fitNo, false
	}
	f = attrFit(ri, q, count, lo, hi)
	if owned {
		if f == fitMatch {
			return fitMatch, true
		}
		return fitUncertain, false
	}
	return f, false
}

// tagState reads the idempotency tag. Distinct values under the key are dup (uncertain); a lone value
// is owned when it equals a non-empty request token and other otherwise.
func tagState(tags []types.Tag, token string) (owned, other, dup bool) {
	var val *string
	for _, t := range tags {
		if aws.ToString(t.Key) != common.IdempotencyTagKey {
			continue
		}
		v := aws.ToString(t.Value)
		if val != nil && *val != v {
			return false, false, true
		}
		val = &v
	}
	if val == nil {
		return false, false, false
	}
	if token != "" && *val == token {
		return true, false, false
	}
	return false, true, false
}

func attrFit(ri types.ReservedInstances, q ec2OfferingQuery, count int, lo, hi time.Time) fit {
	fits := []fit{
		eq(ri.InstanceType, q.instanceType, ""),
		eq(ri.InstanceTenancy, q.tenancy, ""),
		eq(ri.Scope, q.scope, ""),
		eq(ri.OfferingType, q.wantOfferingType, ""),
		eq(ri.OfferingClass, q.offeringClass, ""),
		zoneFit(ri, q),
		presentFit(ri.InstanceCount, func(n int32) bool { return int(n) == count }),
		presentFit(ri.Duration, func(d int64) bool { return d == q.duration }),
		presentFit(ri.Start, func(s time.Time) bool { return purchasecfg.InWindow(s, lo, hi) }),
		stateFit(ri.State),
	}
	if ri.ProductDescription == "" {
		fits = append(fits, fitUncertain)
	} else if !productMatches(string(ri.ProductDescription), string(q.productDesc)) {
		fits = append(fits, fitNo)
	}
	if aws.ToString(ri.ReservedInstancesId) == "" {
		fits = append(fits, fitUncertain)
	}
	f := fitMatch
	for _, x := range fits {
		f = worse(f, x)
	}
	return f
}

// zoneFit: a zonal rec needs the same AZ; a regional rec must carry none.
func zoneFit(ri types.ReservedInstances, q ec2OfferingQuery) fit {
	az := aws.ToString(ri.AvailabilityZone)
	if q.availabilityZone == "" {
		if az != "" {
			return fitNo
		}
		return fitMatch
	}
	return eq(az, q.availabilityZone, "")
}

func presentFit[T any](p *T, ok func(T) bool) fit {
	switch {
	case p == nil:
		return fitUncertain
	case ok(*p):
		return fitMatch
	}
	return fitNo
}

// stateFit: only live states can be reported Found; any other state with matching attributes is doubt.
func stateFit(s types.ReservedInstanceState) fit {
	switch s {
	case types.ReservedInstanceStateActive, types.ReservedInstanceStatePaymentPending, types.ReservedInstanceStateQueued:
		return fitMatch
	}
	return fitUncertain
}
