package redshift

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

func reconcileReq(mut ...func(*common.ReconcileRequest)) common.ReconcileRequest {
	req := common.ReconcileRequest{
		Recommendation: rsIdemRec(),
		Options:        common.PurchaseOptions{RequestedAt: matchT0, ExecutionID: "exec-1"},
		AttemptEndedAt: matchT0.Add(time.Minute),
	}
	for _, m := range mut {
		m(&req)
	}
	return req
}

// reconcileNode builds a node that matches rsIdemRec (offering-1, 1 x ra3.xlplus, 1yr) in the window.
func reconcileNode(id string, mut ...func(*types.ReservedNode)) types.ReservedNode {
	n := baseNode(func(n *types.ReservedNode) {
		n.ReservedNodeId = aws.String(id)
		n.NodeCount = aws.Int32(1)
	})
	for _, m := range mut {
		m(&n)
	}
	return n
}

func nodeState(s string) func(*types.ReservedNode) {
	return func(n *types.ReservedNode) { n.State = aws.String(s) }
}

type reconcileFake struct {
	pages        [][]types.ReservedNode
	describeErr  error
	offeringErr  error
	alwaysMarker bool
}

func (f reconcileFake) client(t *testing.T) (*Client, *MockRedshiftClient) {
	t.Helper()
	m := &MockRedshiftClient{}
	m.Test(t)
	if f.offeringErr != nil {
		m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).Return(nil, f.offeringErr).Maybe()
	} else {
		expectRSOffering(m)
	}
	switch {
	case f.describeErr != nil:
		m.On("DescribeReservedNodes", mock.Anything, mock.Anything).Return(nil, f.describeErr).Maybe()
	case f.alwaysMarker:
		m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
			Return(&redshift.DescribeReservedNodesOutput{Marker: aws.String("more")}, nil).Maybe()
	default:
		expectNodePages(m, f.pages...)
	}
	c := &Client{region: "eu-west-1"}
	c.SetRedshiftAPI(m)
	return c, m
}

func TestReconcilePurchase(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	one := func(ns ...types.ReservedNode) reconcileFake { return reconcileFake{pages: [][]types.ReservedNode{ns}} }

	tests := []struct {
		name         string
		ctx          context.Context
		fake         reconcileFake
		req          common.ReconcileRequest
		wantAPICalls bool
		verdict      common.ReconcileVerdict
		id, reason   string
		candidates   []string
	}{
		{name: "one matching active node", fake: one(reconcileNode("rn-1")), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileFound, id: "rn-1"},
		{name: "match on page 2", fake: reconcileFake{pages: [][]types.ReservedNode{{reconcileNode("rn-x", func(n *types.ReservedNode) { n.NodeCount = aws.Int32(9) })}, {reconcileNode("rn-2")}}},
			req: reconcileReq(), wantAPICalls: true, verdict: common.ReconcileFound, id: "rn-2"},
		{name: "pending-payment node matches", fake: one(reconcileNode("rn-1", nodeState("pending-payment"))), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileFound, id: "rn-1"},
		{name: "no nodes", fake: one(), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "two matches", fake: one(reconcileNode("rn-1"), reconcileNode("rn-2")), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonAmbiguous, candidates: []string{"rn-1", "rn-2"}},
		{name: "count differs", fake: one(reconcileNode("rn-1", func(n *types.ReservedNode) { n.NodeCount = aws.Int32(5) })), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "other offering", fake: one(reconcileNode("rn-1", func(n *types.ReservedNode) { n.ReservedNodeOfferingId = aws.String("other") })), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "started before lo", fake: one(reconcileNode("rn-1", func(n *types.ReservedNode) { n.StartTime = aws.Time(matchT0.Add(-time.Hour)) })), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "started after hi", fake: one(reconcileNode("rn-1", func(n *types.ReservedNode) { n.StartTime = aws.Time(matchT0.Add(time.Hour)) })), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "future start", fake: one(reconcileNode("rn-1", func(n *types.ReservedNode) { n.StartTime = aws.Time(matchT0.Add(24 * time.Hour)) })), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "nil start", fake: one(reconcileNode("rn-1", func(n *types.ReservedNode) { n.StartTime = nil })), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonUncertainCandidate, candidates: []string{"rn-1"}},
		{name: "retired node", fake: one(reconcileNode("rn-1", nodeState("retired"))), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonUncertainCandidate, candidates: []string{"rn-1"}},
		{name: "payment-failed node", fake: one(reconcileNode("rn-1", nodeState("payment-failed"))), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonUncertainCandidate, candidates: []string{"rn-1"}},
		{name: "match plus retired", fake: one(reconcileNode("rn-1"), reconcileNode("rn-2", nodeState("retired"))), req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonUncertainCandidate, candidates: []string{"rn-1", "rn-2"}},
		{name: "describe error", fake: reconcileFake{describeErr: errors.New("boom")}, req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonLookupFailed},
		{name: "page cap", fake: reconcileFake{alwaysMarker: true}, req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonLookupIncomplete},
		{name: "canceled context", ctx: canceled, fake: one(reconcileNode("rn-1")), req: reconcileReq(),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonLookupFailed},
		{name: "offering error", fake: reconcileFake{offeringErr: errors.New("boom")}, req: reconcileReq(),
			wantAPICalls: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonOfferingUnresolved},
		{name: "region mismatch", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.Region = "us-east-1" }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "empty region", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.Region = "" }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "zero count", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.Count = 0 }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "bad term", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.Term = "5yr" }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "zero RequestedAt", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.Options.RequestedAt = time.Time{} }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidWindow},
		{name: "zero AttemptEndedAt", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.AttemptEndedAt = time.Time{} }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidWindow},
		{name: "end before start", fake: one(reconcileNode("rn-1")), req: reconcileReq(func(r *common.ReconcileRequest) { r.AttemptEndedAt = matchT0.Add(-time.Hour) }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, m := tt.fake.client(t)
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			got := c.ReconcilePurchase(ctx, tt.req)

			assert.Equal(t, tt.verdict, got.Verdict)
			assert.Equal(t, tt.id, got.CommitmentID)
			assert.Equal(t, tt.reason, got.Reason)
			assert.Equal(t, tt.candidates, got.CandidateIDs)
			assert.False(t, got.Tagged, "Tagged is EC2-only")
			if tt.verdict == common.ReconcileFound {
				assert.NotEmpty(t, got.Criteria)
			} else {
				assert.Empty(t, got.Criteria)
			}
			if !tt.wantAPICalls {
				m.AssertNotCalled(t, "DescribeReservedNodes", mock.Anything, mock.Anything)
				m.AssertNotCalled(t, "DescribeReservedNodeOfferings", mock.Anything, mock.Anything)
			}
			m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
		})
	}
}

func TestReconcilePurchase_CapDistinctFromLookupFailed(t *testing.T) {
	t.Parallel()
	_, err := (&Client{client: capClient(t)}).describeAllNodes(context.Background())
	require.ErrorIs(t, err, errScanCapReached)
}

func capClient(t *testing.T) *MockRedshiftClient {
	t.Helper()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodesOutput{Marker: aws.String("more")}, nil)
	return m
}
