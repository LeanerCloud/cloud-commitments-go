package redshift

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// All tests here are fixture-based: mocks only, no AWS calls.

var dedupeReqAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func dedupeOpts() common.PurchaseOptions {
	return common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("dedupe", 0), RequestedAt: dedupeReqAt}
}

// dnode builds a node matching rsIdemRec (offering-1, count 1) unless altered.
func dnode(id, state string, start time.Time, alter ...func(*types.ReservedNode)) types.ReservedNode {
	n := types.ReservedNode{
		ReservedNodeId: aws.String(id), ReservedNodeOfferingId: aws.String("offering-1"),
		NodeCount: aws.Int32(1), State: aws.String(state), StartTime: aws.Time(start),
	}
	for _, f := range alter {
		f(&n)
	}
	return n
}

// expectNodePages serves pages of DescribeReservedNodes keyed by marker.
func expectNodePages(m *MockRedshiftClient, pages ...[]types.ReservedNode) {
	for i, nodes := range pages {
		want := ""
		if i > 0 {
			want = fmt.Sprintf("p%d", i)
		}
		out := &redshift.DescribeReservedNodesOutput{ReservedNodes: nodes}
		if i < len(pages)-1 {
			out.Marker = aws.String(fmt.Sprintf("p%d", i+1))
		}
		m.On("DescribeReservedNodes", mock.Anything, mock.MatchedBy(func(in *redshift.DescribeReservedNodesInput) bool {
			return aws.ToString(in.Marker) == want
		})).Return(out, nil).Maybe()
	}
}

func expectBuy(m *MockRedshiftClient) {
	m.On("PurchaseReservedNodeOffering", mock.Anything, mock.Anything).
		Return(&redshift.PurchaseReservedNodeOfferingOutput{ReservedNode: &types.ReservedNode{ReservedNodeId: aws.String("rn-new")}}, nil).Maybe()
}

func newDedupe(pages ...[]types.ReservedNode) (*Client, *MockRedshiftClient) {
	m := &MockRedshiftClient{}
	expectRSOffering(m)
	expectNodePages(m, pages...)
	expectBuy(m)
	return &Client{client: m, region: "eu-west-1"}, m
}

func TestDedupe_Found(t *testing.T) {
	t.Parallel()
	inWindow := dedupeReqAt.Add(time.Minute)
	for _, tc := range []struct {
		name string
		node types.ReservedNode
	}{
		{"active in window", dnode("rn-1", "active", inWindow)},
		{"start exactly at lower bound", dnode("rn-1", "active", dedupeReqAt.Add(-redshiftClockSkew))},
		{"start long after request, no upper bound", dnode("rn-1", "active", dedupeReqAt.Add(72*time.Hour))},
		{"pending-payment", dnode("rn-1", "pending-payment", inWindow)},
		{"exchanging", dnode("rn-1", "exchanging", inWindow)},
		{"nil offering ID", dnode("rn-1", "active", inWindow, func(n *types.ReservedNode) { n.ReservedNodeOfferingId = nil })},
		{"nil node count", dnode("rn-1", "active", inWindow, func(n *types.ReservedNode) { n.NodeCount = nil })},
		{"nil StartTime", dnode("rn-1", "active", inWindow, func(n *types.ReservedNode) { n.StartTime = nil })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m := newDedupe([]types.ReservedNode{tc.node})
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
			require.NoError(t, err)
			assert.True(t, result.Success)
			assert.True(t, result.ExistingCommitment)
			assert.Equal(t, "rn-1", result.CommitmentID)
			m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
		})
	}
}

func TestDedupe_NotFoundPurchases(t *testing.T) {
	t.Parallel()
	inWindow := dedupeReqAt.Add(time.Minute)
	for _, tc := range []struct {
		name string
		node types.ReservedNode
	}{
		{"different offering", dnode("rn-1", "active", inWindow, func(n *types.ReservedNode) { n.ReservedNodeOfferingId = aws.String("offering-2") })},
		{"different count", dnode("rn-1", "active", inWindow, func(n *types.ReservedNode) { n.NodeCount = aws.Int32(2) })},
		{"started before window", dnode("rn-1", "active", dedupeReqAt.Add(-redshiftClockSkew-time.Second))},
		{"retired", dnode("rn-1", "retired", inWindow)},
		{"payment-failed", dnode("rn-1", "payment-failed", inWindow)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m := newDedupe([]types.ReservedNode{tc.node})
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
			require.NoError(t, err)
			assert.False(t, result.ExistingCommitment)
			assert.Equal(t, "rn-new", result.CommitmentID)
			m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
		})
	}
}

func TestDedupe_AmbiguousRefuses(t *testing.T) {
	t.Parallel()
	c, m := newDedupe([]types.ReservedNode{dnode("rn-a", "active", dedupeReqAt), dnode("rn-b", "pending-payment", dedupeReqAt)})
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
	require.Error(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, err.Error(), "rn-a")
	assert.Contains(t, err.Error(), "rn-b")
	assert.Contains(t, err.Error(), "check the account")
	assert.Contains(t, err.Error(), "start a new purchase")
	m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
}

func TestDedupe_LookupErrorRefuses(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	expectRSOffering(m)
	expectBuy(m)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).Return((*redshift.DescribeReservedNodesOutput)(nil), errors.New("access denied"))
	c := &Client{client: m, region: "eu-west-1"}
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
	require.Error(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, err.Error(), "refusing to purchase to avoid a possible double-buy")
	assert.Contains(t, err.Error(), "access denied")
	m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
}

func TestDedupe_PageCap(t *testing.T) {
	t.Parallel()
	other := []types.ReservedNode{dnode("rn-x", "active", dedupeReqAt, func(n *types.ReservedNode) { n.ReservedNodeOfferingId = aws.String("other") })}
	t.Run("cap refuses", func(t *testing.T) {
		pages := make([][]types.ReservedNode, maxReservedNodePages+1)
		for i := range pages {
			pages[i] = other
		}
		c, m := newDedupe(pages...)
		_, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pagination cap reached after 10 pages")
		assert.Contains(t, err.Error(), "refusing to purchase")
		m.AssertNumberOfCalls(t, "DescribeReservedNodes", maxReservedNodePages)
		m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
	})
	t.Run("match on page two", func(t *testing.T) {
		c, m := newDedupe(other, []types.ReservedNode{dnode("rn-2", "active", dedupeReqAt)})
		result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
		require.NoError(t, err)
		assert.Equal(t, "rn-2", result.CommitmentID)
		m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
	})
}

func TestDedupe_TokenWithoutRequestedAtRefuses(t *testing.T) {
	t.Parallel()
	c, m := newDedupe()
	opts := dedupeOpts()
	opts.RequestedAt = time.Time{}
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), opts)
	assert.EqualError(t, err, "RequestedAt is required with an idempotency token: Redshift reserved nodes cannot be tagged, so an earlier purchase is found by start time; refusing to purchase to avoid a possible double-buy")
	assert.False(t, result.Success)
	m.AssertNotCalled(t, "DescribeReservedNodes", mock.Anything, mock.Anything)
	m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
}

func TestDedupe_EmptyTokenDoesNoLookup(t *testing.T) {
	t.Parallel()
	c, m := newDedupe()
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), common.PurchaseOptions{Source: common.PurchaseSourceCLI})
	require.NoError(t, err)
	assert.Equal(t, "rn-new", result.CommitmentID)
	m.AssertNotCalled(t, "DescribeReservedNodes", mock.Anything, mock.Anything)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
}

func TestDedupe_CanceledContextRefusesBeforeScan(t *testing.T) {
	t.Parallel()
	c, m := newDedupe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.findMatchingNodes(ctx, "offering-1", 1, dedupeReqAt)
	assert.ErrorIs(t, err, context.Canceled)
	m.AssertNotCalled(t, "DescribeReservedNodes", mock.Anything, mock.Anything)
}

func unknownOutcome() error {
	return fmt.Errorf("%w: connection reset", common.ErrOutcomeUnknown)
}

func clean4xx() error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusBadRequest}},
		Err:      &smithy.GenericAPIError{Code: "InvalidParameterValue", Message: "bad"},
	}}
}

// recoverMock returns an empty guard lookup first, then the post-error lookup.
func recoverMock(buy func(*MockRedshiftClient), second ...types.ReservedNode) (*Client, *MockRedshiftClient) {
	m := &MockRedshiftClient{}
	expectRSOffering(m)
	buy(m)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).Return(&redshift.DescribeReservedNodesOutput{}, nil).Once()
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).Return(&redshift.DescribeReservedNodesOutput{ReservedNodes: second}, nil).Maybe()
	return &Client{client: m, region: "eu-west-1"}, m
}

func buyFails(err error) func(*MockRedshiftClient) {
	return func(m *MockRedshiftClient) {
		m.On("PurchaseReservedNodeOffering", mock.Anything, mock.Anything).Return((*redshift.PurchaseReservedNodeOfferingOutput)(nil), err)
	}
}

func buyEmpty(m *MockRedshiftClient) {
	m.On("PurchaseReservedNodeOffering", mock.Anything, mock.Anything).Return(&redshift.PurchaseReservedNodeOfferingOutput{}, nil)
}

func TestDedupe_RecoverAfterUnknownOutcome(t *testing.T) {
	t.Parallel()
	one := dnode("rn-bought", "payment-pending", dedupeReqAt.Add(time.Second))
	two := dnode("rn-bought2", "active", dedupeReqAt.Add(time.Second))
	for _, tc := range []struct {
		name  string
		buy   func(*MockRedshiftClient)
		nodes []types.ReservedNode
		adopt bool
	}{
		{"timeout then one match adopts", buyFails(unknownOutcome()), []types.ReservedNode{one}, true},
		{"empty 200 then one match adopts", buyEmpty, []types.ReservedNode{one}, true},
		{"timeout then none keeps original error", buyFails(unknownOutcome()), nil, false},
		{"timeout then ambiguous keeps original error", buyFails(unknownOutcome()), []types.ReservedNode{one, two}, false},
		{"empty 200 then none keeps original error", buyEmpty, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m := recoverMock(tc.buy, tc.nodes...)
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
			m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
			m.AssertNumberOfCalls(t, "DescribeReservedNodes", 2)
			if tc.adopt {
				require.NoError(t, err)
				assert.True(t, result.Success)
				assert.True(t, result.ExistingCommitment)
				assert.Equal(t, "rn-bought", result.CommitmentID)
				assert.NoError(t, result.Error)
				return
			}
			assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
			assert.False(t, result.Success)
		})
	}
}

func TestDedupe_RecoverLookupErrorKeepsOriginalError(t *testing.T) {
	t.Parallel()
	c, m := recoverMock(buyFails(unknownOutcome()))
	m.ExpectedCalls = m.ExpectedCalls[:len(m.ExpectedCalls)-1]
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).Return((*redshift.DescribeReservedNodesOutput)(nil), errors.New("throttled"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
	assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
}

func TestDedupe_Clean4xxDoesNoRecoveryLookup(t *testing.T) {
	t.Parallel()
	c, m := recoverMock(buyFails(clean4xx()), dnode("rn-bought", "active", dedupeReqAt))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), dedupeOpts())
	require.Error(t, err)
	assert.NotErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
	m.AssertNumberOfCalls(t, "DescribeReservedNodes", 1)
}

func TestDedupe_RecoverLookupIsDetachedAndBounded(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &MockRedshiftClient{}
	expectRSOffering(m)
	m.On("PurchaseReservedNodeOffering", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { cancel() }).
		Return((*redshift.PurchaseReservedNodeOfferingOutput)(nil), unknownOutcome())
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).Return(&redshift.DescribeReservedNodesOutput{}, nil).Once()
	var lookupErr error
	var deadline time.Time
	var hasDeadline bool
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			lookupCtx := args.Get(0).(context.Context)
			lookupErr = lookupCtx.Err()
			deadline, hasDeadline = lookupCtx.Deadline()
		}).
		Return(&redshift.DescribeReservedNodesOutput{ReservedNodes: []types.ReservedNode{dnode("rn-bought", "active", dedupeReqAt)}}, nil)
	c := &Client{client: m, region: "eu-west-1"}

	result, err := c.PurchaseCommitment(ctx, rsIdemRec(), dedupeOpts())
	require.NoError(t, err)
	assert.Equal(t, "rn-bought", result.CommitmentID)
	assert.NoError(t, lookupErr, "the lookup must survive the caller's cancellation")
	require.True(t, hasDeadline, "the lookup must carry a deadline")
	assert.LessOrEqual(t, time.Until(deadline), recoverLookupTimeout)
	assert.Equal(t, 30*time.Second, recoverLookupTimeout)
}
