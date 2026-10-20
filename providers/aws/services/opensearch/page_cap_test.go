package opensearch

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetExistingCommitments_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockOpenSearchClient{}
	m.Test(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
		Return(&opensearch.DescribeReservedInstancesOutput{NextToken: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedInstances", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockOpenSearchClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetExistingCommitments(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedInstances", mock.Anything, mock.Anything)
}

func TestGetExistingCommitments_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockOpenSearchClient{}
	m.Test(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
		Return(&opensearch.DescribeReservedInstancesOutput{NextToken: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
		Return(&opensearch.DescribeReservedInstancesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedInstances", maxListPages)
}

func TestGetExistingCommitments_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockOpenSearchClient{}
	m.Test(t)
	m.On("DescribeReservedInstances", mock.Anything, mock.MatchedBy(func(in *opensearch.DescribeReservedInstancesInput) bool { return in.NextToken == nil })).
		Return(&opensearch.DescribeReservedInstancesOutput{NextToken: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedInstances", mock.Anything, mock.MatchedBy(func(in *opensearch.DescribeReservedInstancesInput) bool { return aws.ToString(in.NextToken) == "t2" })).
		Return(&opensearch.DescribeReservedInstancesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedInstances", 2)
}
