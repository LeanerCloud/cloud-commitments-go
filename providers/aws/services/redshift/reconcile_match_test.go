package redshift

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
)

var matchT0 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

const oneYearNodeSeconds = int32(12 * 2592000)

func baseNode(mut ...func(*types.ReservedNode)) types.ReservedNode {
	n := types.ReservedNode{
		ReservedNodeId: aws.String("rn-1"), ReservedNodeOfferingId: aws.String("offering-1"),
		NodeCount: aws.Int32(2), NodeType: aws.String("ra3.xlplus"), Duration: aws.Int32(oneYearNodeSeconds),
		StartTime: aws.Time(matchT0.Add(time.Minute)), State: aws.String("active"),
	}
	for _, m := range mut {
		m(&n)
	}
	return n
}

func TestNodeFit(t *testing.T) {
	t.Parallel()
	lo, hi := matchT0.Add(-5*time.Minute), matchT0.Add(10*time.Minute)
	set := func(f func(*types.ReservedNode)) []func(*types.ReservedNode) { return []func(*types.ReservedNode){f} }

	tests := []struct {
		name string
		mut  []func(*types.ReservedNode)
		want fit
	}{
		{"all equal", nil, fitMatch},
		{"state pending-payment", set(func(n *types.ReservedNode) { n.State = aws.String("pending-payment") }), fitMatch},
		{"state payment-pending", set(func(n *types.ReservedNode) { n.State = aws.String("payment-pending") }), fitMatch},
		{"start at lo", set(func(n *types.ReservedNode) { n.StartTime = aws.Time(lo) }), fitMatch},
		{"start at hi", set(func(n *types.ReservedNode) { n.StartTime = aws.Time(hi) }), fitMatch},
		{"start before lo", set(func(n *types.ReservedNode) { n.StartTime = aws.Time(lo.Add(-time.Second)) }), fitNo},
		{"start after hi", set(func(n *types.ReservedNode) { n.StartTime = aws.Time(hi.Add(time.Second)) }), fitNo},
		{"future start", set(func(n *types.ReservedNode) { n.StartTime = aws.Time(matchT0.Add(24 * time.Hour)) }), fitNo},
		{"nil start", set(func(n *types.ReservedNode) { n.StartTime = nil }), fitUncertain},
		{"other offering", set(func(n *types.ReservedNode) { n.ReservedNodeOfferingId = aws.String("x") }), fitNo},
		{"nil offering", set(func(n *types.ReservedNode) { n.ReservedNodeOfferingId = nil }), fitUncertain},
		{"other count", set(func(n *types.ReservedNode) { n.NodeCount = aws.Int32(3) }), fitNo},
		{"nil count", set(func(n *types.ReservedNode) { n.NodeCount = nil }), fitUncertain},
		{"other node type", set(func(n *types.ReservedNode) { n.NodeType = aws.String("dc2.large") }), fitNo},
		{"nil node type", set(func(n *types.ReservedNode) { n.NodeType = nil }), fitUncertain},
		{"other duration", set(func(n *types.ReservedNode) { n.Duration = aws.Int32(3 * oneYearNodeSeconds) }), fitNo},
		{"nil duration", set(func(n *types.ReservedNode) { n.Duration = nil }), fitUncertain},
		{"state retired", set(func(n *types.ReservedNode) { n.State = aws.String("retired") }), fitUncertain},
		{"state payment-failed", set(func(n *types.ReservedNode) { n.State = aws.String("payment-failed") }), fitUncertain},
		{"state exchanging", set(func(n *types.ReservedNode) { n.State = aws.String("exchanging") }), fitUncertain},
		{"state empty", set(func(n *types.ReservedNode) { n.State = aws.String("") }), fitUncertain},
		{"state nil", set(func(n *types.ReservedNode) { n.State = nil }), fitUncertain},
		{"nil id", set(func(n *types.ReservedNode) { n.ReservedNodeId = nil }), fitUncertain},
		{"empty id", set(func(n *types.ReservedNode) { n.ReservedNodeId = aws.String("") }), fitUncertain},
		{"retired and other count is no", []func(*types.ReservedNode){
			func(n *types.ReservedNode) { n.State = aws.String("retired") },
			func(n *types.ReservedNode) { n.NodeCount = aws.Int32(3) },
		}, fitNo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := baseNode(tt.mut...)
			assert.Equal(t, tt.want, nodeFit(&n, "offering-1", 2, "ra3.xlplus", 12, lo, hi))
		})
	}
}
