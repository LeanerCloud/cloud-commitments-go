package ec2

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestRIFit(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	lo, hi := start.Add(-time.Minute), start.Add(time.Minute)
	const token = "tok-1"
	const count = 3

	regional := ec2OfferingQuery{
		instanceType: types.InstanceTypeM5Large, productDesc: types.RIProductDescriptionLinuxUnix,
		tenancy: types.TenancyDefault, scope: types.ScopeRegional,
		wantOfferingType: types.OfferingTypeValuesNoUpfront,
		offeringClass:    types.OfferingClassTypeStandard, duration: OneYearSeconds,
	}
	zonal := regional
	zonal.scope = types.ScopeAvailabilityZone
	zonal.availabilityZone = "us-east-1a"
	class := types.OfferingClassTypeStandard

	baseRI := func(mut ...func(*types.ReservedInstances)) types.ReservedInstances {
		ri := types.ReservedInstances{
			ReservedInstancesId: aws.String("ri-1"), InstanceType: types.InstanceTypeM5Large,
			InstanceCount: aws.Int32(count), ProductDescription: types.RIProductDescriptionLinuxUnix,
			InstanceTenancy: types.TenancyDefault, Scope: types.ScopeRegional,
			Duration: aws.Int64(OneYearSeconds), OfferingType: types.OfferingTypeValuesNoUpfront,
			OfferingClass: types.OfferingClassTypeStandard, Start: aws.Time(start),
			State: types.ReservedInstanceStateActive,
		}
		for _, m := range mut {
			m(&ri)
		}
		return ri
	}
	tag := func(vals ...string) func(*types.ReservedInstances) {
		return func(ri *types.ReservedInstances) {
			for _, v := range vals {
				ri.Tags = append(ri.Tags, types.Tag{Key: aws.String(common.IdempotencyTagKey), Value: aws.String(v)})
			}
		}
	}
	zonalRI := func(mut ...func(*types.ReservedInstances)) types.ReservedInstances {
		return baseRI(append([]func(*types.ReservedInstances){func(ri *types.ReservedInstances) {
			ri.Scope = types.ScopeAvailabilityZone
			ri.AvailabilityZone = aws.String("us-east-1a")
		}}, mut...)...)
	}
	set := func(f func(*types.ReservedInstances)) func(*types.ReservedInstances) { return f }

	tests := []struct {
		name       string
		ri         types.ReservedInstances
		q          ec2OfferingQuery
		token      string
		want       fit
		wantTagged bool
	}{
		{"all equal untagged", baseRI(), regional, token, fitMatch, false},
		{"zonal all equal", zonalRI(), zonal, token, fitMatch, false},
		{"vpc suffix ignored", baseRI(set(func(ri *types.ReservedInstances) { ri.ProductDescription = "Linux/UNIX (Amazon VPC)" })), regional, token, fitMatch, false},

		{"instance type differs", baseRI(set(func(ri *types.ReservedInstances) { ri.InstanceType = types.InstanceTypeC5Large })), regional, token, fitNo, false},
		{"count differs", baseRI(set(func(ri *types.ReservedInstances) { ri.InstanceCount = aws.Int32(count + 1) })), regional, token, fitNo, false},
		{"product differs", baseRI(set(func(ri *types.ReservedInstances) { ri.ProductDescription = types.RIProductDescriptionWindows })), regional, token, fitNo, false},
		{"tenancy differs", baseRI(set(func(ri *types.ReservedInstances) { ri.InstanceTenancy = types.TenancyDedicated })), regional, token, fitNo, false},
		{"scope differs", baseRI(set(func(ri *types.ReservedInstances) { ri.Scope = types.ScopeAvailabilityZone })), regional, token, fitNo, false},
		{"az differs", zonalRI(set(func(ri *types.ReservedInstances) { ri.AvailabilityZone = aws.String("us-east-1b") })), zonal, token, fitNo, false},
		{"regional rec with AZ set", baseRI(set(func(ri *types.ReservedInstances) { ri.AvailabilityZone = aws.String("us-east-1a") })), regional, token, fitNo, false},
		{"duration differs", baseRI(set(func(ri *types.ReservedInstances) { ri.Duration = aws.Int64(3 * OneYearSeconds) })), regional, token, fitNo, false},
		{"offering type differs", baseRI(set(func(ri *types.ReservedInstances) { ri.OfferingType = types.OfferingTypeValuesAllUpfront })), regional, token, fitNo, false},
		{"class differs", baseRI(set(func(ri *types.ReservedInstances) { ri.OfferingClass = types.OfferingClassTypeConvertible })), regional, token, fitNo, false},

		{"instance type missing", baseRI(set(func(ri *types.ReservedInstances) { ri.InstanceType = "" })), regional, token, fitUncertain, false},
		{"count missing", baseRI(set(func(ri *types.ReservedInstances) { ri.InstanceCount = nil })), regional, token, fitUncertain, false},
		{"product missing", baseRI(set(func(ri *types.ReservedInstances) { ri.ProductDescription = "" })), regional, token, fitUncertain, false},
		{"tenancy missing", baseRI(set(func(ri *types.ReservedInstances) { ri.InstanceTenancy = "" })), regional, token, fitUncertain, false},
		{"scope missing", baseRI(set(func(ri *types.ReservedInstances) { ri.Scope = "" })), regional, token, fitUncertain, false},
		{"zonal rec with AZ missing", zonalRI(set(func(ri *types.ReservedInstances) { ri.AvailabilityZone = nil })), zonal, token, fitUncertain, false},
		{"duration missing", baseRI(set(func(ri *types.ReservedInstances) { ri.Duration = nil })), regional, token, fitUncertain, false},
		{"offering type missing", baseRI(set(func(ri *types.ReservedInstances) { ri.OfferingType = "" })), regional, token, fitUncertain, false},
		{"class missing", baseRI(set(func(ri *types.ReservedInstances) { ri.OfferingClass = "" })), regional, token, fitUncertain, false},
		{"id nil", baseRI(set(func(ri *types.ReservedInstances) { ri.ReservedInstancesId = nil })), regional, token, fitUncertain, false},
		{"id empty", baseRI(set(func(ri *types.ReservedInstances) { ri.ReservedInstancesId = aws.String("") })), regional, token, fitUncertain, false},
		{"id empty but count differs", baseRI(set(func(ri *types.ReservedInstances) {
			ri.ReservedInstancesId = nil
			ri.InstanceCount = aws.Int32(count + 1)
		})), regional, token, fitNo, false},

		{"start at lo", baseRI(set(func(ri *types.ReservedInstances) { ri.Start = aws.Time(lo) })), regional, token, fitMatch, false},
		{"start at hi", baseRI(set(func(ri *types.ReservedInstances) { ri.Start = aws.Time(hi) })), regional, token, fitMatch, false},
		{"start before lo", baseRI(set(func(ri *types.ReservedInstances) { ri.Start = aws.Time(lo.Add(-time.Nanosecond)) })), regional, token, fitNo, false},
		{"start after hi", baseRI(set(func(ri *types.ReservedInstances) { ri.Start = aws.Time(hi.Add(time.Nanosecond)) })), regional, token, fitNo, false},
		{"start nil", baseRI(set(func(ri *types.ReservedInstances) { ri.Start = nil })), regional, token, fitUncertain, false},

		{"state active", baseRI(), regional, token, fitMatch, false},
		{"state payment-pending", baseRI(set(func(ri *types.ReservedInstances) { ri.State = types.ReservedInstanceStatePaymentPending })), regional, token, fitMatch, false},
		{"state queued", baseRI(set(func(ri *types.ReservedInstances) { ri.State = types.ReservedInstanceStateQueued })), regional, token, fitMatch, false},
		{"state retired", baseRI(set(func(ri *types.ReservedInstances) { ri.State = types.ReservedInstanceStateRetired })), regional, token, fitUncertain, false},
		{"state payment-failed", baseRI(set(func(ri *types.ReservedInstances) { ri.State = types.ReservedInstanceStatePaymentFailed })), regional, token, fitUncertain, false},
		{"state queued-deleted", baseRI(set(func(ri *types.ReservedInstances) { ri.State = types.ReservedInstanceStateQueuedDeleted })), regional, token, fitUncertain, false},
		{"state empty", baseRI(set(func(ri *types.ReservedInstances) { ri.State = "" })), regional, token, fitUncertain, false},

		{"tag other value", baseRI(tag("other")), regional, token, fitNo, false},
		{"tag same value attributes equal", baseRI(tag(token)), regional, token, fitMatch, true},
		{"same-token tag count differs", baseRI(tag(token), set(func(ri *types.ReservedInstances) { ri.InstanceCount = aws.Int32(count + 1) })), regional, token, fitUncertain, false},
		{"same-token tag outside window", baseRI(tag(token), set(func(ri *types.ReservedInstances) { ri.Start = aws.Time(hi.Add(time.Hour)) })), regional, token, fitUncertain, false},
		{"duplicate tag key two values", baseRI(tag(token, "other")), regional, token, fitUncertain, false},
		{"duplicate tag key same value", baseRI(tag(token, token)), regional, token, fitMatch, true},
		{"empty request token with token tag", baseRI(tag("other")), regional, "", fitNo, false},
		{"empty request token with empty tag value", baseRI(tag("")), regional, "", fitNo, false},
		{"empty request token untagged", baseRI(), regional, "", fitMatch, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, tagged := riFit(tt.ri, tt.q, class, count, tt.token, lo, hi)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantTagged, tagged)
		})
	}
}
