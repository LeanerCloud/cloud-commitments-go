package ec2

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
)

func TestOfferingMismatch(t *testing.T) {
	t.Parallel()
	base := offeringWant{
		instanceType: "m5.large", productDesc: "Linux/UNIX", tenancy: types.TenancyDefault,
		scope: types.ScopeRegional, duration: OneYearSeconds, class: types.OfferingClassTypeConvertible,
	}
	with := func(mw func(*offeringWant)) offeringWant { w := base; mw(&w); return w }
	off := func(mo ...func(*types.ReservedInstancesOffering)) types.ReservedInstancesOffering {
		return fullOffering("o", "", mo...)
	}
	tests := []struct {
		name string
		o    types.ReservedInstancesOffering
		w    offeringWant
		want string
	}{
		{"full match", off(), base, ""},
		{"zone mismatch", off(), with(func(w *offeringWant) { w.az = "us-east-1a" }), "zone"},
		{"class standard", off(func(o *types.ReservedInstancesOffering) { o.OfferingClass = types.OfferingClassTypeStandard }), base, "class"},
		{"class unset", off(func(o *types.ReservedInstancesOffering) { o.OfferingClass = "" }), base, "class"},
		{"class unchecked when not requested", off(func(o *types.ReservedInstancesOffering) { o.OfferingClass = "" }), with(func(w *offeringWant) { w.class = "" }), ""},
		{"tenancy dedicated", off(func(o *types.ReservedInstancesOffering) { o.InstanceTenancy = types.TenancyDedicated }), base, "tenancy"},
		{"tenancy unset", off(func(o *types.ReservedInstancesOffering) { o.InstanceTenancy = "" }), base, "tenancy"},
		{"duration 3yr", off(func(o *types.ReservedInstancesOffering) { o.Duration = aws.Int64(3 * OneYearSeconds) }), base, "duration"},
		{"duration nil", off(func(o *types.ReservedInstancesOffering) { o.Duration = nil }), base, "duration"},
		{"offering type differs", off(), with(func(w *offeringWant) { w.offeringType = types.OfferingTypeValuesAllUpfront }), "offering-type"},
		{"offering type unset on offering", off(func(o *types.ReservedInstancesOffering) { o.OfferingType = "" }), with(func(w *offeringWant) { w.offeringType = types.OfferingTypeValuesNoUpfront }), "offering-type"},
		{"offering type unchecked when not requested", off(), base, ""},
		{"instance type differs", off(func(o *types.ReservedInstancesOffering) { o.InstanceType = types.InstanceTypeC5Large }), base, "instance-type"},
		{"instance type unset on offering", off(func(o *types.ReservedInstancesOffering) { o.InstanceType = "" }), base, ""},
		{"instance type unchecked when not requested", off(), with(func(w *offeringWant) { w.instanceType = "" }), ""},
		{"zonal want, regional offering", off(), with(func(w *offeringWant) { w.scope = types.ScopeAvailabilityZone }), "scope"},
		{"regional want, offering scope unset", off(func(o *types.ReservedInstancesOffering) { o.Scope = "" }), base, ""},
		{"product differs", off(func(o *types.ReservedInstancesOffering) { o.ProductDescription = types.RIProductDescriptionWindows }), base, "product"},
		{"offering with VPC suffix", off(func(o *types.ReservedInstancesOffering) {
			o.ProductDescription = types.RIProductDescriptionLinuxUnixAmazonVpc
		}), base, ""},
		{"want with VPC suffix", off(), with(func(w *offeringWant) { w.productDesc = "Linux/UNIX (Amazon VPC)" }), ""},
		{"product unset on offering", off(func(o *types.ReservedInstancesOffering) { o.ProductDescription = "" }), base, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, offeringMismatch(tc.o, tc.w))
		})
	}
}

func TestOfferingSkips_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", offeringSkips{}.String())
	assert.Equal(t, "class=1, tenancy=2", offeringSkips{"tenancy": 2, "class": 1}.String())
	assert.Equal(t, "", offeringSkips{}.note())
	assert.Equal(t, "; 3 offerings skipped: class=1, tenancy=2", offeringSkips{"tenancy": 2, "class": 1}.note())
}
