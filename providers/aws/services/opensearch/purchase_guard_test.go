package opensearch

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The purchaseguard rules (issue #10), asserted at the opensearch purchase path. Each
// mock is bounded with Test(t): an unexpected AWS call fails the test instead
// of panicking.

func newGuardMock(t *testing.T) *MockOpenSearchClient {
	t.Helper()
	m := &MockOpenSearchClient{}
	m.Test(t)
	expectOSOffering(m)
	return m
}

func guardClient(m *MockOpenSearchClient) *Client { return &Client{client: m, region: "eu-west-1"} }

// No token: the guard never looks the reservation up and the purchase runs.
func TestPurchaseGuard_EmptyTokenSkipsLookup(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).Return(&opensearch.PurchaseReservedInstanceOfferingOutput{ReservedInstanceId: aws.String("os-new")}, nil).Once()

	result, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	m.AssertNotCalled(t, "DescribeReservedInstances", mock.Anything, mock.Anything)
	m.AssertNumberOfCalls(t, "PurchaseReservedInstanceOffering", 1)
}

// With a token, the guard looks up before buying, and the lookup runs strictly
// before the purchase call.
func TestPurchaseGuard_LookupPrecedesPurchase(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	var order []string
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { order = append(order, "lookup") }).
		Return(&opensearch.DescribeReservedInstancesOutput{}, nil).Once()
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { order = append(order, "purchase") }).
		Return(&opensearch.PurchaseReservedInstanceOfferingOutput{ReservedInstanceId: aws.String("os-new")}, nil).Once()

	_, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-order", 0)})

	require.NoError(t, err)
	assert.Equal(t, []string{"lookup", "purchase"}, order)
}

// A lookup failure refuses the purchase with the shared guard's error text.
func TestPurchaseGuard_LookupErrorRefusesWithLabel(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).Return((*opensearch.DescribeReservedInstancesOutput)(nil), fmt.Errorf("access denied")).Once()

	_, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-label", 0)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "idempotency lookup failed before OpenSearch RI purchase (refusing to purchase to avoid a possible double-buy)")
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
}

// AlreadyExists with a token but a failing re-lookup: the original purchase
// error is reported, never a false success.
func TestPurchaseGuard_AlreadyExistsRelookupErrorReportsPurchaseError(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).Return(&opensearch.DescribeReservedInstancesOutput{}, nil).Once()
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).Return((*opensearch.PurchaseReservedInstanceOfferingOutput)(nil), &types.ResourceAlreadyExistsException{}).Once()
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).Return((*opensearch.DescribeReservedInstancesOutput)(nil), fmt.Errorf("throttled")).Once()

	result, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-relookup", 0)})

	require.Error(t, err)
	var already *types.ResourceAlreadyExistsException
	assert.True(t, errors.As(err, &already), "the original AlreadyExists error must be reported")
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
}

// AlreadyExists with a token but a re-lookup that misses: same, no false success.
func TestPurchaseGuard_AlreadyExistsRelookupMissReportsPurchaseError(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).Return(&opensearch.DescribeReservedInstancesOutput{}, nil).Twice()
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).Return((*opensearch.PurchaseReservedInstanceOfferingOutput)(nil), &types.ResourceAlreadyExistsException{}).Once()

	result, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-miss", 0)})

	require.Error(t, err)
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
}

// No token: AlreadyExists is never recovered (and nothing is looked up).
func TestPurchaseGuard_AlreadyExistsWithoutTokenNotRecovered(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).Return((*opensearch.PurchaseReservedInstanceOfferingOutput)(nil), &types.ResourceAlreadyExistsException{}).Once()

	result, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{})

	require.Error(t, err)
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	m.AssertNotCalled(t, "DescribeReservedInstances", mock.Anything, mock.Anything)
}

// A purchase error that is not AlreadyExists triggers no recovery lookup.
func TestPurchaseGuard_OtherPurchaseErrorNotRecovered(t *testing.T) {
	t.Parallel()
	m := newGuardMock(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).Return(&opensearch.DescribeReservedInstancesOutput{}, nil).Once()
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).Return((*opensearch.PurchaseReservedInstanceOfferingOutput)(nil), fmt.Errorf("boom")).Once()

	result, err := guardClient(m).PurchaseCommitment(context.Background(), osIdemRec(), common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("exec-other", 0)})

	require.Error(t, err)
	assert.False(t, result.Success)
	m.AssertNumberOfCalls(t, "DescribeReservedInstances", 1)
}
