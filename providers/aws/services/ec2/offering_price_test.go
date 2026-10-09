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

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// All fixtures here are FIXTURE-BASED: SDK-typed responses shaped like
// DescribeReservedInstancesOfferings output, served by a fake client. They are
// not verified against live AWS.

func priceRec(term string) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceCompute, ResourceType: "t3.micro", Term: term, Count: 3,
		PaymentOption: "partial-upfront", Region: "us-east-1",
		Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}
}

func priceOffering(mut func(*types.ReservedInstancesOffering)) types.ReservedInstancesOffering {
	o := types.ReservedInstancesOffering{
		ReservedInstancesOfferingId: aws.String("off-price"),
		InstanceType:                types.InstanceTypeT3Micro,
		ProductDescription:          types.RIProductDescriptionLinuxUnix,
		InstanceTenancy:             types.TenancyDefault,
		OfferingType:                types.OfferingTypeValuesPartialUpfront,
		Duration:                    aws.Int64(94608000),
		FixedPrice:                  aws.Float32(500),
		UsagePrice:                  aws.Float32(0),
		CurrencyCode:                types.CurrencyCodeValuesUsd,
		RecurringCharges:            []types.RecurringCharge{{Amount: aws.Float64(0.03), Frequency: types.RecurringChargeFrequencyHourly}},
		// Marketplace field: number of reservations available at a price. Not a price.
		PricingDetails: []types.PricingDetail{{Price: aws.Float64(5), Count: aws.Int32(12)}},
	}
	if mut != nil {
		mut(&o)
	}
	return o
}

func detailsFor(t *testing.T, rec common.Recommendation, o types.ReservedInstancesOffering) (*common.OfferingDetails, error) {
	t.Helper()
	m := &MockEC2Client{}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{o}}, nil)
	return (&Client{client: m, region: "us-east-1"}).GetOfferingDetails(context.Background(), rec)
}

func TestGetOfferingDetails_PricesFromFixedPriceAndRecurringCharges(t *testing.T) {
	t.Parallel()
	d, err := detailsFor(t, priceRec("3yr"), priceOffering(nil))
	require.NoError(t, err)
	assert.Equal(t, 500.0, d.UpfrontCost, "FixedPrice, not the marketplace PricingDetails price")
	assert.InDelta(t, 0.03, d.RecurringCost, 1e-12)
	assert.InDelta(t, 500+0.03*26280, d.TotalCost, 1e-9)
	assert.InDelta(t, d.TotalCost/26280, d.EffectiveHourlyRate, 1e-12)
	assert.Equal(t, "USD", d.Currency)
}

func TestGetOfferingDetails_ShapesAndUsagePriceRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mut    func(*types.ReservedInstancesOffering)
		total  float64
		hourly float64
		err    string
	}{
		{"all upfront", func(o *types.ReservedInstancesOffering) { o.RecurringCharges = nil }, 500, 0, ""},
		{"usage 0 plus hourly charge", nil, 500 + 0.03*26280, 0.03, ""},
		{"usage only, no charges", func(o *types.ReservedInstancesOffering) {
			o.RecurringCharges, o.UsagePrice = nil, aws.Float32(0.05)
		}, 500 + 0.05*26280, 0.05, ""},
		{"both nonzero", func(o *types.ReservedInstancesOffering) { o.UsagePrice = aws.Float32(0.05) }, 0, 0, "refusing to guess"},
		{"non-hourly charge", func(o *types.ReservedInstancesOffering) {
			o.RecurringCharges = []types.RecurringCharge{{Amount: aws.Float64(1), Frequency: "Monthly"}}
		}, 0, 0, "only \"Hourly\""},
		{"empty currency", func(o *types.ReservedInstancesOffering) { o.CurrencyCode = "" }, 0, 0, "no currency"},
		{"nil fixed price", func(o *types.ReservedInstancesOffering) { o.FixedPrice = nil }, 0, 0, "no FixedPrice"},
		{"duration disagrees with term", func(o *types.ReservedInstancesOffering) { o.Duration = aws.Int64(31536000) }, 0, 0, "does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := detailsFor(t, priceRec("3yr"), priceOffering(tc.mut))
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				assert.Nil(t, d)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.total, d.TotalCost, 1e-9)
			assert.InDelta(t, tc.hourly, d.RecurringCost, 1e-12)
		})
	}
}

// The priced upfront and the purchase result cost must agree, including for a
// float32 FixedPrice that is not exactly representable.
func TestGetOfferingDetails_UpfrontMatchesPurchaseCost(t *testing.T) {
	t.Parallel()
	o := priceOffering(func(o *types.ReservedInstancesOffering) { o.FixedPrice = aws.Float32(1234.56) })
	d, err := detailsFor(t, priceRec("3yr"), o)
	require.NoError(t, err)

	m := &MockEC2Client{}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{o}}, nil)
	m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-1")}, nil)
	m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil)
	rec := priceRec("3yr")
	rec.Count = 1
	res, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	require.NoError(t, err)
	require.NotNil(t, res.Cost)
	assert.Equal(t, 1234.56, d.UpfrontCost)
	assert.Equal(t, d.UpfrontCost, *res.Cost)
}

func TestGetOfferingDetails_OneYearTermAndMultipleHourlyChargesSum(t *testing.T) {
	t.Parallel()
	d, err := detailsFor(t, priceRec("1yr"), priceOffering(func(o *types.ReservedInstancesOffering) {
		o.Duration = aws.Int64(31536000)
		o.RecurringCharges = []types.RecurringCharge{
			{Amount: aws.Float64(0.03), Frequency: types.RecurringChargeFrequencyHourly},
			{Amount: aws.Float64(0.02), Frequency: types.RecurringChargeFrequencyHourly},
		}
	}))
	require.NoError(t, err)
	assert.InDelta(t, 0.05, d.RecurringCost, 1e-12, "multiple Hourly charges are summed")
	assert.InDelta(t, 500+0.05*8760, d.TotalCost, 1e-9)
}
