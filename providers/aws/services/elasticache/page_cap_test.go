package elasticache

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetExistingCommitments_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockElastiCacheClient{}
	m.Test(t)
	m.On("DescribeReservedCacheNodes", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOutput{Marker: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedCacheNodes", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockElastiCacheClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetExistingCommitments(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedCacheNodes", mock.Anything, mock.Anything)
}

func TestGetExistingCommitments_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockElastiCacheClient{}
	m.Test(t)
	m.On("DescribeReservedCacheNodes", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOutput{Marker: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedCacheNodes", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedCacheNodes", maxListPages)
}

func TestGetExistingCommitments_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockElastiCacheClient{}
	m.Test(t)
	m.On("DescribeReservedCacheNodes", mock.Anything, mock.MatchedBy(func(in *elasticache.DescribeReservedCacheNodesInput) bool { return in.Marker == nil })).
		Return(&elasticache.DescribeReservedCacheNodesOutput{Marker: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedCacheNodes", mock.Anything, mock.MatchedBy(func(in *elasticache.DescribeReservedCacheNodesInput) bool { return aws.ToString(in.Marker) == "t2" })).
		Return(&elasticache.DescribeReservedCacheNodesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedCacheNodes", 2)
}

func TestGetValidResourceTypes_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockElastiCacheClient{}
	m.Test(t)
	m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{Marker: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedCacheNodesOfferings", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockElastiCacheClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetValidResourceTypes(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything)
}

func TestGetValidResourceTypes_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockElastiCacheClient{}
	m.Test(t)
	m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{Marker: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
		Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedCacheNodesOfferings", maxListPages)
}

func TestGetValidResourceTypes_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockElastiCacheClient{}
	m.Test(t)
	m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.MatchedBy(func(in *elasticache.DescribeReservedCacheNodesOfferingsInput) bool { return in.Marker == nil })).
		Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{Marker: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.MatchedBy(func(in *elasticache.DescribeReservedCacheNodesOfferingsInput) bool {
		return aws.ToString(in.Marker) == "t2"
	})).
		Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedCacheNodesOfferings", 2)
}
