package redshift

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetExistingCommitments_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodesOutput{Marker: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedNodes", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockRedshiftClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetExistingCommitments(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedNodes", mock.Anything, mock.Anything)
}

func TestGetExistingCommitments_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodesOutput{Marker: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodes", maxListPages)
}

func TestGetExistingCommitments_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodes", mock.Anything, mock.MatchedBy(func(in *redshift.DescribeReservedNodesInput) bool { return in.Marker == nil })).
		Return(&redshift.DescribeReservedNodesOutput{Marker: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedNodes", mock.Anything, mock.MatchedBy(func(in *redshift.DescribeReservedNodesInput) bool { return aws.ToString(in.Marker) == "t2" })).
		Return(&redshift.DescribeReservedNodesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodes", 2)
}

func TestGetValidResourceTypes_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{Marker: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedNodeOfferings", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockRedshiftClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetValidResourceTypes(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedNodeOfferings", mock.Anything, mock.Anything)
}

func TestGetValidResourceTypes_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{Marker: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodeOfferings", maxListPages)
}

func TestGetValidResourceTypes_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	m.Test(t)
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.MatchedBy(func(in *redshift.DescribeReservedNodeOfferingsInput) bool { return in.Marker == nil })).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{Marker: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.MatchedBy(func(in *redshift.DescribeReservedNodeOfferingsInput) bool { return aws.ToString(in.Marker) == "t2" })).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodeOfferings", 2)
}
