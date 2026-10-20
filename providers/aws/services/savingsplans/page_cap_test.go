package savingsplans

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGetExistingCommitments_PageCapErrorsInsteadOfPartial(t *testing.T) {
	t.Parallel()
	m := &MockSavingsPlansClient{}
	m.Test(t)
	m.On("DescribeSavingsPlans", mock.Anything, mock.Anything).
		Return(&savingsplans.DescribeSavingsPlansOutput{NextToken: aws.String("more")}, nil).Times(maxCommitmentPages)
	c := &Client{client: m, region: "us-east-1"}
	got, err := c.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap reached")
	assert.Nil(t, got)
	m.AssertNumberOfCalls(t, "DescribeSavingsPlans", maxCommitmentPages)
}

func TestGetExistingCommitments_LastAllowedPageWithoutTokenSucceeds(t *testing.T) {
	t.Parallel()
	m := &MockSavingsPlansClient{}
	m.Test(t)
	m.On("DescribeSavingsPlans", mock.Anything, mock.Anything).
		Return(&savingsplans.DescribeSavingsPlansOutput{NextToken: aws.String("more")}, nil).Times(maxCommitmentPages - 1)
	m.On("DescribeSavingsPlans", mock.Anything, mock.Anything).
		Return(&savingsplans.DescribeSavingsPlansOutput{}, nil).Once()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeSavingsPlans", maxCommitmentPages)
}

func TestGetExistingCommitments_CanceledCtx(t *testing.T) {
	t.Parallel()
	m := &MockSavingsPlansClient{}
	m.Test(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{client: m, region: "us-east-1"}
	_, err := c.GetExistingCommitments(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	m.AssertNotCalled(t, "DescribeSavingsPlans", mock.Anything, mock.Anything)
}
