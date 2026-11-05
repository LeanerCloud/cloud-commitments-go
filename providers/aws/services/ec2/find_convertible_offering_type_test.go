package ec2

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #553: the exchange target must have the source RI's payment option, never
// whichever offering AWS happens to list first.

func typedOffering(id string, ot types.OfferingTypeValues) types.ReservedInstancesOffering {
	return types.ReservedInstancesOffering{
		ReservedInstancesOfferingId: aws.String(id), InstanceType: types.InstanceTypeM5Large,
		Duration: aws.Int64(OneYearSeconds), OfferingType: ot, ProductDescription: types.RIProductDescriptionLinuxUnix,
		InstanceTenancy: types.TenancyDefault, OfferingClass: types.OfferingClassTypeConvertible, Scope: types.ScopeRegional,
	}
}

func typedFindParams(offeringType string) FindConvertibleOfferingParams {
	return FindConvertibleOfferingParams{InstanceType: "m5.large", ProductDescription: "Linux/UNIX",
		Tenancy: "default", Scope: "Region", Duration: OneYearSeconds, OfferingType: offeringType}
}

func TestFindConvertibleOffering_MatchesTheSourceOfferingType(t *testing.T) {
	t.Parallel()
	all := typedOffering("off-all", types.OfferingTypeValuesAllUpfront)
	partial := typedOffering("off-partial", types.OfferingTypeValuesPartialUpfront)
	none := typedOffering("off-none", types.OfferingTypeValuesNoUpfront)
	orders := [][]types.ReservedInstancesOffering{
		{all, partial, none}, {none, all, partial}, {partial, none, all}, {all, none, partial}, {none, partial, all},
	}
	want := map[types.OfferingTypeValues]string{
		types.OfferingTypeValuesAllUpfront: "off-all", types.OfferingTypeValuesPartialUpfront: "off-partial",
		types.OfferingTypeValuesNoUpfront: "off-none",
	}
	for _, page := range orders {
		for ot, id := range want {
			m := &MockEC2Client{}
			var sent *ec2.DescribeReservedInstancesOfferingsInput
			m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) { sent = args.Get(1).(*ec2.DescribeReservedInstancesOfferingsInput) }).
				Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: page}, nil)
			got, err := (&Client{client: m, region: "us-east-1"}).FindConvertibleOffering(context.Background(), typedFindParams(string(ot)))
			require.NoError(t, err, "type %s", ot)
			assert.Equal(t, id, got, "type %s from page order %v", ot, page)
			require.NotNil(t, sent)
			assert.Equal(t, ot, sent.OfferingType, "the request filters on the source offering type")
		}
	}
}

func TestFindConvertibleOffering_RefusesMissingOrUnknownOfferingType(t *testing.T) {
	t.Parallel()
	for _, ot := range []string{"", "  ", " No Upfront ", "no-upfront", "Heavy Utilization"} {
		m := matchingOfferingMock()
		_, err := (&Client{client: m, region: "us-east-1"}).FindConvertibleOffering(context.Background(), typedFindParams(ot))
		require.Error(t, err, "offering type %q", ot)
		assert.Contains(t, err.Error(), "offering type")
		m.AssertNotCalled(t, "DescribeReservedInstancesOfferings", mock.Anything, mock.Anything)
	}
}

func TestFindConvertibleOffering_NoOfferingOfTheSourceTypeFailsLoud(t *testing.T) {
	t.Parallel()
	m := &MockEC2Client{}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).Return(&ec2.DescribeReservedInstancesOfferingsOutput{
		ReservedInstancesOfferings: []types.ReservedInstancesOffering{
			typedOffering("off-all", types.OfferingTypeValuesAllUpfront),
			typedOffering("off-partial", types.OfferingTypeValuesPartialUpfront),
		},
	}, nil)
	_, err := (&Client{client: m, region: "us-east-1"}).FindConvertibleOffering(context.Background(), typedFindParams(string(types.OfferingTypeValuesNoUpfront)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no convertible offering found")
}
