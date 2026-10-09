package memorydb

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	"github.com/aws/aws-sdk-go-v2/service/memorydb/types"

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

// The SDK offering type has no currency field and no UsagePrice, so the usage
// cases are not applicable; USD is assumed outside cn- regions.
func TestGetOfferingDetails_Pricing(t *testing.T) {
	for _, tc := range []priceCase{
		{name: "3yr term", term: "3yr", duration: threeYear, usage: 0, charges: []float64{0.05}, currency: "USD", region: "", err: "", hourly: 0.05, hours: 26280},
		{name: "two hourly charges are summed", term: "1yr", duration: oneYear, usage: 0, charges: []float64{0.03, 0.02}, currency: "USD", region: "", err: "", hourly: 0.05, hours: 8760},
		{name: "3yr offering vs 1yr request", term: "1yr", duration: threeYear, usage: 0, charges: []float64{0.05}, currency: "USD", region: "", err: "does not match", hourly: 0, hours: 0},
		{name: "cn region has no assumable currency", term: "1yr", duration: oneYear, region: "cn-northwest-1", err: "not billed in USD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := common.Recommendation{
				Service: common.ServiceCache, ResourceType: "db.r6gd.xlarge", PaymentOption: "partial-upfront", Term: tc.term,
				Details: &common.CacheDetails{Engine: "redis", NodeType: "db.r6gd.xlarge"},
			}
			region := tc.region
			if region == "" {
				region = "us-east-1"
			}
			charges := make([]types.RecurringCharge, 0, len(tc.charges))
			for _, a := range tc.charges {
				charges = append(charges, types.RecurringCharge{RecurringChargeAmount: a, RecurringChargeFrequency: aws.String("Hourly")})
			}
			m := &MockMemoryDBClient{}
			m.On("DescribeReservedNodesOfferings", mock.Anything, mock.Anything).
				Return(&memorydb.DescribeReservedNodesOfferingsOutput{ReservedNodesOfferings: []types.ReservedNodesOffering{{
					ReservedNodesOfferingId: aws.String("off-1"), NodeType: aws.String("db.r6gd.xlarge"), Duration: tc.duration,
					OfferingType: aws.String("Partial Upfront"), FixedPrice: 100, RecurringCharges: charges,
				}}}, nil)
			d, err := (&Client{client: m, region: region}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.hourly, d.RecurringCost, 1e-12)
			assert.InDelta(t, 100+tc.hourly*tc.hours, d.TotalCost, 1e-9)
			assert.Equal(t, "USD", d.Currency)
		})
	}
}
