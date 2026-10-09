package rds

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// FIXTURE-BASED: SDK-typed responses served by a fake client, not live AWS.
type priceCase struct {
	name     string
	term     string
	duration int32
	usage    float64
	charges  []float64 // Hourly amounts
	currency string
	region   string
	err      string
	hourly   float64
	hours    float64
}

const (
	oneYear   = 31536000
	threeYear = 94608000
)

func TestGetOfferingDetails_Pricing(t *testing.T) {
	for _, tc := range []priceCase{
		{name: "hourly charge, usage 0", term: "1yr", duration: oneYear, usage: 0, charges: []float64{0.05}, currency: "USD", region: "", err: "", hourly: 0.05, hours: 8760},
		{name: "3yr term", term: "3yr", duration: threeYear, usage: 0, charges: []float64{0.05}, currency: "USD", region: "", err: "", hourly: 0.05, hours: 26280},
		{name: "usage only, no charges", term: "1yr", duration: oneYear, usage: 0.05, charges: nil, currency: "USD", region: "", err: "", hourly: 0.05, hours: 8760},
		{name: "two hourly charges are summed", term: "1yr", duration: oneYear, usage: 0, charges: []float64{0.03, 0.02}, currency: "USD", region: "", err: "", hourly: 0.05, hours: 8760},
		{name: "3yr offering vs 1yr request", term: "1yr", duration: threeYear, usage: 0, charges: []float64{0.05}, currency: "USD", region: "", err: "no offerings found", hourly: 0, hours: 0},
		{name: "usage and charge both nonzero", term: "1yr", duration: oneYear, usage: 0.05, charges: []float64{0.05}, currency: "USD", region: "", err: "refusing to guess", hourly: 0, hours: 0},
		{name: "empty currency", term: "1yr", duration: oneYear, charges: []float64{0.05}, currency: "", err: "no currency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := common.Recommendation{
				Service: common.ServiceRelationalDB, ResourceType: "db.m6g.large", PaymentOption: "no-upfront", Term: tc.term,
				Details: &common.DatabaseDetails{Engine: "postgres", AZConfig: "multi-az"},
			}
			charges := make([]types.RecurringCharge, 0, len(tc.charges))
			for _, a := range tc.charges {
				charges = append(charges, types.RecurringCharge{RecurringChargeAmount: aws.Float64(a), RecurringChargeFrequency: aws.String("Hourly")})
			}
			m := &MockRDSClient{}
			m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
				Return(&rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{{
					ReservedDBInstancesOfferingId: aws.String("off-1"), DBInstanceClass: aws.String("db.m6g.large"),
					Duration: aws.Int32(tc.duration), OfferingType: aws.String("No Upfront"), MultiAZ: aws.Bool(true),
					ProductDescription: aws.String("postgresql"), FixedPrice: aws.Float64(100), UsagePrice: aws.Float64(tc.usage),
					CurrencyCode: aws.String(tc.currency), RecurringCharges: charges,
				}}}, nil)
			d, err := (&Client{client: m, region: "us-east-2"}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.hourly, d.RecurringCost, 1e-12)
			assert.InDelta(t, 100+tc.hourly*tc.hours, d.TotalCost, 1e-9)
		})
	}
}
