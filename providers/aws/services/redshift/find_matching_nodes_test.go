package redshift

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Characterization of findMatchingNodes: pins the paging, cap, ctx and field-matching behavior.
func TestFindMatchingNodes_Characterization(t *testing.T) {
	t.Parallel()
	active := "active"
	match := func(id string) types.ReservedNode { return dnode(id, active, dedupeReqAt) }
	old := dnode("old", active, dedupeReqAt.Add(-time.Hour))
	otherOffering := dnode("other", active, dedupeReqAt, func(n *types.ReservedNode) { n.ReservedNodeOfferingId = aws.String("x") })
	otherCount := dnode("cnt", active, dedupeReqAt, func(n *types.ReservedNode) { n.NodeCount = aws.Int32(9) })
	noStart := dnode("nostart", active, dedupeReqAt, func(n *types.ReservedNode) { n.StartTime = nil })
	noOffering := dnode("nooff", active, dedupeReqAt, func(n *types.ReservedNode) { n.ReservedNodeOfferingId = nil })
	noCount := dnode("nocnt", active, dedupeReqAt, func(n *types.ReservedNode) { n.NodeCount = nil })
	retired := dnode("ret", "retired", dedupeReqAt)

	tests := []struct {
		name  string
		pages [][]types.ReservedNode
		want  []string
	}{
		{"one page", [][]types.ReservedNode{{match("a"), old, otherOffering, otherCount, retired}}, []string{"a"}},
		{"two pages", [][]types.ReservedNode{{match("a")}, {match("b")}}, []string{"a", "b"}},
		{"missing fields still match", [][]types.ReservedNode{{noStart, noOffering, noCount}}, []string{"nostart", "nooff", "nocnt"}},
		{"empty", [][]types.ReservedNode{{}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MockRedshiftClient{}
			m.Test(t)
			expectNodePages(m, tt.pages...)
			c := &Client{client: m, region: "eu-west-1"}
			got, err := c.findMatchingNodes(context.Background(), "offering-1", 1, dedupeReqAt)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("cap reached", func(t *testing.T) {
		t.Parallel()
		m := &MockRedshiftClient{}
		m.Test(t)
		m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
			Return(&redshift.DescribeReservedNodesOutput{Marker: aws.String("more")}, nil)
		c := &Client{client: m, region: "eu-west-1"}
		_, err := c.findMatchingNodes(context.Background(), "offering-1", 1, dedupeReqAt)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pagination cap reached")
		m.AssertNumberOfCalls(t, "DescribeReservedNodes", maxReservedNodePages)
	})

	t.Run("ctx canceled mid-scan", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := &MockRedshiftClient{}
		m.Test(t)
		m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { cancel() }).
			Return(&redshift.DescribeReservedNodesOutput{ReservedNodes: []types.ReservedNode{match("a")}, Marker: aws.String("p1")}, nil)
		c := &Client{client: m, region: "eu-west-1"}
		got, err := c.findMatchingNodes(ctx, "offering-1", 1, dedupeReqAt)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, got)
		m.AssertNumberOfCalls(t, "DescribeReservedNodes", 1)
	})
}
