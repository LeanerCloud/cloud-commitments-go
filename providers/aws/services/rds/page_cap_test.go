package rds

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetExistingCommitments_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	m.Test(t)
	m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOutput{Marker: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstances", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockRDSClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetExistingCommitments(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedDBInstances", mock.Anything, mock.Anything)
}

func TestGetExistingCommitments_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	m.Test(t)
	m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOutput{Marker: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstances", maxListPages)
}

func TestGetExistingCommitments_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	m.Test(t)
	m.On("DescribeReservedDBInstances", mock.Anything, mock.MatchedBy(func(in *rds.DescribeReservedDBInstancesInput) bool { return in.Marker == nil })).
		Return(&rds.DescribeReservedDBInstancesOutput{Marker: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedDBInstances", mock.Anything, mock.MatchedBy(func(in *rds.DescribeReservedDBInstancesInput) bool { return aws.ToString(in.Marker) == "t2" })).
		Return(&rds.DescribeReservedDBInstancesOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstances", 2)
}

func TestGetValidResourceTypes_PageCapAndCtx(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	m.Test(t)
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{Marker: aws.String("more")}, nil).Times(maxListPages)
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", maxListPages)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m2 := &MockRDSClient{}
	m2.Test(t)
	c2 := &Client{client: m2, region: "us-east-1"}
	_, err = c2.GetValidResourceTypes(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m2.AssertNotCalled(t, "DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything)
}

func TestGetValidResourceTypes_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	m.Test(t)
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{Marker: aws.String("more")}, nil).Times(maxListPages - 1)
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", maxListPages)
}

func TestGetValidResourceTypes_PassesTokenAcrossPages(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	m.Test(t)
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.MatchedBy(func(in *rds.DescribeReservedDBInstancesOfferingsInput) bool { return in.Marker == nil })).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{Marker: aws.String("t2")}, nil).Once()
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.MatchedBy(func(in *rds.DescribeReservedDBInstancesOfferingsInput) bool { return aws.ToString(in.Marker) == "t2" })).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", 2)
}
