package ec2

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetValidResourceTypes_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockEC2Client{}
	m.Test(t)
	m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeInstanceTypeOfferingsOutput{NextToken: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeInstanceTypeOfferings", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockEC2Client{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetValidResourceTypes(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeInstanceTypeOfferings", mock.Anything, mock.Anything)
}

func TestGetValidResourceTypes_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockEC2Client{}
	m.Test(t)
	m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeInstanceTypeOfferingsOutput{NextToken: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeInstanceTypeOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeInstanceTypeOfferings", maxListPages)
}

func TestGetValidResourceTypes_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockEC2Client{}
	m.Test(t)
	m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.MatchedBy(func(in *ec2.DescribeInstanceTypeOfferingsInput) bool { return in.NextToken == nil })).
		Return(&ec2.DescribeInstanceTypeOfferingsOutput{NextToken: aws.String("t2")}, nil).Once()
	m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.MatchedBy(func(in *ec2.DescribeInstanceTypeOfferingsInput) bool { return aws.ToString(in.NextToken) == "t2" })).
		Return(&ec2.DescribeInstanceTypeOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeInstanceTypeOfferings", 2)
}
