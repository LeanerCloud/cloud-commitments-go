package ec2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// realPurchaseAPI sends only the purchase through the real SDK client that
// NewClient builds (purchasecfg retry/timeout config), so the SDK retryer is the
// code under test. Every other call goes to the embedded mock.
type realPurchaseAPI struct {
	API
	real *ec2.Client
}

func (r *realPurchaseAPI) PurchaseReservedInstancesOffering(ctx context.Context, in *ec2.PurchaseReservedInstancesOfferingInput, optFns ...func(*ec2.Options)) (*ec2.PurchaseReservedInstancesOfferingOutput, error) {
	return r.real.PurchaseReservedInstancesOffering(ctx, in, optFns...)
}

// TestPurchaseCommitment_LostResponseIsNotRetriedBySDK reproduces MON-02: AWS
// commits the purchase but the response is lost (connection dropped). The EC2
// purchase API has no ClientToken, so a second SDK-level attempt buys a second,
// untagged Reserved Instance. Exactly one purchase request may reach AWS.
func TestPurchaseCommitment_LostResponseIsNotRetriedBySDK(t *testing.T) {
	var purchases atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Action=PurchaseReservedInstancesOffering") {
			purchases.Add(1)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = conn.Close() // request received (RI "committed"), response never delivered
	}))
	defer srv.Close()

	c := NewClient(aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
	})
	mockEC2 := &MockEC2Client{}
	c.client = &realPurchaseAPI{API: mockEC2, real: c.client.(*ec2.Client)}
	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
			ReservedInstancesOfferingId: aws.String("offering-123"), InstanceType: types.InstanceTypeT3Micro,
			Duration: aws.Int64(94608000), OfferingType: types.OfferingTypeValuesPartialUpfront,
			ProductDescription: types.RIProductDescriptionLinuxUnix, InstanceTenancy: types.TenancyDefault,
			FixedPrice: aws.Float32(100),
		}}}, nil)

	rec := common.Recommendation{ResourceType: "t3.micro", Count: 2, PaymentOption: "partial-upfront", Term: "3yr",
		Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"}}
	result, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})

	require.Error(t, err, "a purchase whose response was lost must surface as an error, not a success")
	assert.False(t, result.Success)
	assert.ErrorIs(t, err, common.ErrOutcomeUnknown, "a lost response must be reported as outcome-unknown, not as a definite failure")
	assert.EqualValues(t, 1, purchases.Load(), "the SDK must not re-send a non-idempotent EC2 purchase (no ClientToken)")
}

// TestPurchaseCommitment_EmptyResponseIsOutcomeUnknown: a 200 without a
// ReservedInstancesId means the buy most likely happened, so it must not read
// as a definite failure that a caller may safely retry.
func TestPurchaseCommitment_EmptyResponseIsOutcomeUnknown(t *testing.T) {
	m := &MockEC2Client{}
	c := &Client{client: m, region: "us-east-1"}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
			ReservedInstancesOfferingId: aws.String("offering-123"), InstanceType: types.InstanceTypeT3Micro,
			Duration: aws.Int64(94608000), OfferingType: types.OfferingTypeValuesPartialUpfront,
			ProductDescription: types.RIProductDescriptionLinuxUnix, InstanceTenancy: types.TenancyDefault,
		}}}, nil)
	m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).Return(&ec2.PurchaseReservedInstancesOfferingOutput{}, nil)

	rec := common.Recommendation{ResourceType: "t3.micro", Count: 1, PaymentOption: "partial-upfront", Term: "3yr",
		Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"}}
	_, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
}
