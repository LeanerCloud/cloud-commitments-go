package ec2

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func sdkRI(id, product string, tenancy types.Tenancy, scope types.Scope, az *string) types.ReservedInstances {
	return types.ReservedInstances{
		ReservedInstancesId: aws.String(id),
		InstanceType:        types.InstanceTypeM5Large,
		InstanceCount:       aws.Int32(2),
		State:               types.ReservedInstanceStateActive,
		Start:               aws.Time(time.Now().Add(-time.Hour)),
		End:                 aws.Time(time.Now().Add(24 * time.Hour)),
		ProductDescription:  types.RIProductDescription(product),
		InstanceTenancy:     tenancy,
		Scope:               scope,
		AvailabilityZone:    az,
	}
}

func listingClient(ris ...types.ReservedInstances) *Client {
	m := &MockEC2Client{}
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOutput{ReservedInstances: ris}, nil)
	return &Client{client: m, region: "us-east-1"}
}

func TestGetExistingCommitments_ReportsPlatformTenancyScope(t *testing.T) {
	t.Parallel()
	client := listingClient(
		sdkRI("ri-regional", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil),
		sdkRI("ri-zonal", "Windows", types.TenancyDedicated, types.ScopeAvailabilityZone, aws.String("us-east-1a")),
		types.ReservedInstances{ReservedInstancesId: aws.String("ri-nil")},
	)
	got, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, "Linux/UNIX", got[0].Platform)
	assert.Equal(t, "default", got[0].Tenancy)
	assert.Equal(t, "Region", got[0].Scope)
	assert.Empty(t, got[0].AvailabilityZone)

	assert.Equal(t, "Windows", got[1].Platform)
	assert.Equal(t, "dedicated", got[1].Tenancy)
	assert.Equal(t, "Availability Zone", got[1].Scope)
	assert.Equal(t, "us-east-1a", got[1].AvailabilityZone)

	assert.Empty(t, got[2].Platform)
	assert.Empty(t, got[2].Tenancy)
	assert.Empty(t, got[2].Scope)
	assert.Empty(t, got[2].AvailabilityZone)
}

// parserRec mirrors the ComputeDetails parseEC2Details builds from Cost Explorer.
func parserRec(scope, az string) common.Recommendation {
	return common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceEC2, ResourceType: "m5.large",
		Region: "us-east-1", Count: 1,
		Details: &common.ComputeDetails{InstanceType: "m5.large", Platform: "Linux/UNIX",
			Tenancy: "default", Scope: scope, AvailabilityZone: az},
	}
}

func TestListingFeedsDuplicateChecker(t *testing.T) {
	t.Parallel()
	linuxRegional := sdkRI("ri-1", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil)
	windowsRegional := sdkRI("ri-2", "Windows", types.TenancyDefault, types.ScopeRegional, nil)
	linuxZonal := sdkRI("ri-3", "Linux/UNIX", types.TenancyDefault, types.ScopeAvailabilityZone, aws.String("us-east-1a"))

	tests := []struct {
		name     string
		ri       types.ReservedInstances
		rec      common.Recommendation
		suppress bool
	}{
		{"windows RI does not suppress linux rec", windowsRegional, parserRec("Region", ""), false},
		{"zonal RI does not suppress regional rec", linuxZonal, parserRec("Region", ""), false},
		{"regional RI suppresses zonal rec", linuxRegional, parserRec("Availability Zone", "us-east-1a"), true},
		{"matching linux regional RI suppresses", linuxRegional, parserRec("Region", ""), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed, filtered, err := recfilter.NewDuplicateChecker(0).
				AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{tt.rec}, listingClient(tt.ri))
			require.NoError(t, err)
			if tt.suppress {
				assert.Empty(t, passed)
				assert.Len(t, filtered, 1)
			} else {
				assert.Len(t, passed, 1)
				assert.Empty(t, filtered)
			}
		})
	}
}

func TestListConvertibleReservedInstances_PriceReported(t *testing.T) {
	t.Parallel()
	unpriced := sdkRI("ri-unpriced", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil)
	fixedOnly := sdkRI("ri-fixed", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil)
	fixedOnly.FixedPrice = aws.Float32(0)
	fixedOnly.UsagePrice = aws.Float32(0.1)
	recurring := sdkRI("ri-recurring", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil)
	recurring.FixedPrice = aws.Float32(0)
	recurring.RecurringCharges = []types.RecurringCharge{{Amount: aws.Float64(0.2)}}
	noAmount := sdkRI("ri-noamount", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil)
	noAmount.FixedPrice = aws.Float32(0)
	noAmount.RecurringCharges = []types.RecurringCharge{{}}
	noFixed := sdkRI("ri-nofixed", "Linux/UNIX", types.TenancyDefault, types.ScopeRegional, nil)
	noFixed.UsagePrice = aws.Float32(0.1)

	got, err := listingClient(unpriced, fixedOnly, recurring, noAmount, noFixed).
		ListConvertibleReservedInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 5)

	assert.False(t, got[0].PriceReported, "all price fields nil")
	assert.True(t, got[1].PriceReported, "fixed and usage price reported")
	assert.True(t, got[2].PriceReported, "fixed price and recurring charge reported")
	assert.False(t, got[3].PriceReported, "recurring entry without amount")
	assert.False(t, got[4].PriceReported, "nil FixedPrice")
}
