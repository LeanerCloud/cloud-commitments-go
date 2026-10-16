package ec2

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type offMut = func(*types.ReservedInstancesOffering)

func matchRec() common.Recommendation {
	return common.Recommendation{
		ResourceType: "m5.large", Count: 1, PaymentOption: "no-upfront", Term: "1yr",
		Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}
}

// offeringsMock ignores every request filter, as a misbehaving AWS would.
func offeringsMock(offs ...types.ReservedInstancesOffering) *MockEC2Client {
	m := &MockEC2Client{}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: offs}, nil)
	m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-x")}, nil).Maybe()
	m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil).Maybe()
	return m
}

func TestEC2OfferingQueryWant(t *testing.T) {
	t.Parallel()
	q := ec2OfferingQuery{
		instanceType: types.InstanceTypeC5Xlarge, productDesc: types.RIProductDescriptionWindows,
		tenancy: types.TenancyDedicated, scope: types.ScopeAvailabilityZone, availabilityZone: "eu-west-1c",
		duration: 94608000, wantOfferingType: types.OfferingTypeValuesPartialUpfront,
		offeringClass: types.OfferingClassTypeStandard,
	}
	assert.Equal(t, offeringWant{
		instanceType: "c5.xlarge", productDesc: "Windows", tenancy: "dedicated", scope: "Availability Zone",
		duration: 94608000, class: "standard", offeringType: "Partial Upfront", az: "eu-west-1c",
	}, q.want())
	assert.Equal(t, offeringWant{}, ec2OfferingQuery{}.want())
}

// The request filters these fields, but AWS returning something else must not be bought.
func TestPurchaseCommitment_WrongOfferingIsNotPurchased(t *testing.T) {
	t.Parallel()
	for name, mut := range map[string]offMut{
		"tenancy":  func(o *types.ReservedInstancesOffering) { o.InstanceTenancy = types.TenancyDedicated },
		"class":    func(o *types.ReservedInstancesOffering) { o.OfferingClass = types.OfferingClassTypeStandard },
		"duration": func(o *types.ReservedInstancesOffering) { o.Duration = aws.Int64(94608000) },
		"product":  func(o *types.ReservedInstancesOffering) { o.ProductDescription = types.RIProductDescriptionWindows },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := offeringsMock(fullOffering("wrong", "", mut))
			_, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), matchRec(), common.PurchaseOptions{})
			require.ErrorContains(t, err, "failed to find offering")
			assert.ErrorContains(t, err, name+"=1")
			m.AssertNotCalled(t, "PurchaseReservedInstancesOffering", mock.Anything, mock.Anything)
		})
	}
}

func TestFindOffering_SkipsWrongTenancyThenPicksMatch(t *testing.T) {
	t.Parallel()
	var got []*ec2.DescribeReservedInstancesOfferingsInput
	m := pagedOfferingsMock(&got,
		[]types.ReservedInstancesOffering{fullOffering("dedicated", "", func(o *types.ReservedInstancesOffering) { o.InstanceTenancy = types.TenancyDedicated })},
		[]types.ReservedInstancesOffering{fullOffering("good", "")})
	o, err := (&Client{client: m, region: "us-east-1"}).findOffering(context.Background(), matchRec(), "", "")
	require.NoError(t, err)
	assert.Equal(t, "good", aws.ToString(o.ReservedInstancesOfferingId))
	assert.Len(t, got, 2)
}

func TestFindOffering_NotFoundReportsSkips(t *testing.T) {
	t.Parallel()
	m := offeringsMock(
		fullOffering("a", "", func(o *types.ReservedInstancesOffering) { o.InstanceTenancy = types.TenancyDedicated }),
		fullOffering("b", "", func(o *types.ReservedInstancesOffering) { o.OfferingClass = types.OfferingClassTypeStandard }),
		fullOffering("c", "", func(o *types.ReservedInstancesOffering) { o.OfferingClass, o.Duration = "", nil }))
	_, err := (&Client{client: m, region: "us-east-1"}).findOffering(context.Background(), matchRec(), "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no offerings found")
	assert.Contains(t, err.Error(), "m5.large")
	assert.Contains(t, err.Error(), "3 offerings skipped: class=2, tenancy=1")
}

func TestFindOffering_PaginationCapReportsSkips(t *testing.T) {
	t.Parallel()
	m := &MockEC2Client{}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			NextToken: aws.String("more"),
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				fullOffering("w", "", func(o *types.ReservedInstancesOffering) { o.InstanceTenancy = types.TenancyDedicated })},
		}, nil)
	_, err := (&Client{client: m, region: "us-east-1"}).findOffering(context.Background(), matchRec(), "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Contains(t, err.Error(), "tenancy=5")
}

func TestScanEC2OfferingPage_CountsSkipsByReason(t *testing.T) {
	t.Parallel()
	q := ec2OfferingQuery{tenancy: types.TenancyDefault}
	skips := offeringSkips{}
	offs := []types.ReservedInstancesOffering{
		{InstanceTenancy: types.TenancyDedicated},
		{}, // unset tenancy is a mismatch too
	}
	assert.Nil(t, scanEC2OfferingPage(offs, q, skips))
	assert.Equal(t, offeringSkips{"tenancy": 2}, skips)
}

func TestDescribeInputFromQuery_SendsTenancyClassDurationProduct(t *testing.T) {
	t.Parallel()
	q := ec2OfferingQuery{
		instanceType: types.InstanceTypeM5Large, productDesc: types.RIProductDescriptionLinuxUnix,
		tenancy: types.TenancyDedicated, scope: types.ScopeAvailabilityZone, availabilityZone: "us-east-1b",
		duration: 31536000, wantOfferingType: types.OfferingTypeValuesNoUpfront,
		offeringClass: types.OfferingClassTypeStandard,
	}
	in := describeInputFromQuery(q, nil)
	assert.Equal(t, types.TenancyDedicated, in.InstanceTenancy)
	assert.Equal(t, types.OfferingClassTypeStandard, in.OfferingClass)
	assert.EqualValues(t, 31536000, aws.ToInt64(in.MinDuration))
	assert.EqualValues(t, 31536000, aws.ToInt64(in.MaxDuration))
	assert.Equal(t, types.RIProductDescriptionLinuxUnix, in.ProductDescription)
	assert.Equal(t, types.InstanceTypeM5Large, in.InstanceType)
	assert.Equal(t, types.OfferingTypeValuesNoUpfront, in.OfferingType)
	assert.Equal(t, "us-east-1b", aws.ToString(in.AvailabilityZone))
}

func TestFindOffering_VPCSuffixedProductStillMatches(t *testing.T) {
	t.Parallel()
	m := offeringsMock(fullOffering("vpc", "", func(o *types.ReservedInstancesOffering) {
		o.ProductDescription = "Linux/UNIX (Amazon VPC)"
	}))
	o, err := (&Client{client: m, region: "us-east-1"}).findOffering(context.Background(), matchRec(), "", "")
	require.NoError(t, err)
	assert.Equal(t, "vpc", aws.ToString(o.ReservedInstancesOfferingId))
}
