package redshift

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// realPurchaseAPI sends only the purchase through the real SDK client that
// NewClient builds (purchasecfg retry/timeout config), so the SDK retryer is the
// code under test. Every other call goes to the embedded mock.
type realPurchaseAPI struct {
	API
	real *redshift.Client
}

func (r *realPurchaseAPI) PurchaseReservedNodeOffering(ctx context.Context, in *redshift.PurchaseReservedNodeOfferingInput, optFns ...func(*redshift.Options)) (*redshift.PurchaseReservedNodeOfferingOutput, error) {
	return r.real.PurchaseReservedNodeOffering(ctx, in, optFns...)
}

// TestPurchaseCommitment_LostResponseIsNotRetriedBySDK reproduces MON-02 for
// Redshift: PurchaseReservedNodeOffering has no ClientToken and no
// caller-supplied ID, so a second SDK-level attempt after a lost response buys
// a second reserved node. Exactly one purchase request may reach AWS.
func TestPurchaseCommitment_LostResponseIsNotRetriedBySDK(t *testing.T) {
	var purchases atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Action=PurchaseReservedNodeOffering") {
			purchases.Add(1)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = conn.Close() // request received (node "committed"), response never delivered
	}))
	defer srv.Close()

	c := NewClient(aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
		BaseEndpoint: aws.String(srv.URL),
	})
	mockRS := &MockRedshiftClient{}
	c.client = &realPurchaseAPI{API: mockRS, real: c.client.(*redshift.Client)}
	mockRS.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: []types.ReservedNodeOffering{{
			ReservedNodeOfferingId: aws.String("offering-123"), NodeType: aws.String("dc2.large"),
			Duration: aws.Int32(31536000), ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"),
			FixedPrice: aws.Float64(500),
		}}}, nil)

	rec := common.Recommendation{ResourceType: "dc2.large", Count: 2, PaymentOption: "all-upfront", Term: "1yr",
		Details: common.DataWarehouseDetails{NodeType: "dc2.large", NumberOfNodes: 2}}
	result, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})

	require.Error(t, err, "a purchase whose response was lost must surface as an error, not a success")
	assert.False(t, result.Success)
	assert.ErrorIs(t, err, purchasecfg.ErrOutcomeUnknown, "a lost response must be reported as outcome-unknown, not as a definite failure")
	assert.EqualValues(t, 1, purchases.Load(), "the SDK must not re-send a non-idempotent Redshift purchase (no ClientToken)")
}

// TestPurchaseCommitment_EmptyResponseIsOutcomeUnknown: a 200 without a
// ReservedNode means the buy most likely happened, so it must not read as a
// definite failure that a caller may safely retry.
func TestPurchaseCommitment_EmptyResponseIsOutcomeUnknown(t *testing.T) {
	m := &MockRedshiftClient{}
	c := &Client{client: m, region: "us-east-1"}
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: []types.ReservedNodeOffering{{
			ReservedNodeOfferingId: aws.String("offering-123"), NodeType: aws.String("dc2.large"),
			Duration: aws.Int32(31536000), ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"),
			FixedPrice: aws.Float64(500),
		}}}, nil)
	m.On("PurchaseReservedNodeOffering", mock.Anything, mock.Anything).Return(&redshift.PurchaseReservedNodeOfferingOutput{}, nil)

	rec := common.Recommendation{ResourceType: "dc2.large", Count: 1, PaymentOption: "all-upfront", Term: "1yr",
		Details: common.DataWarehouseDetails{NodeType: "dc2.large", NumberOfNodes: 1}}
	_, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	assert.ErrorIs(t, err, purchasecfg.ErrOutcomeUnknown)
}
