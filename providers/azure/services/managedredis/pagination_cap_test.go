package managedredis

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingReservationsPager reports More() until limit pages were requested,
// so a missing cap shows up as calls == limit instead of a hung test.
type countingReservationsPager struct {
	calls, limit int
}

func (p *countingReservationsPager) More() bool { return p.calls < p.limit }
func (p *countingReservationsPager) NextPage(_ context.Context) (armconsumption.ReservationsDetailsClientListResponse, error) {
	p.calls++
	return armconsumption.ReservationsDetailsClientListResponse{}, nil
}

func TestGetExistingCommitments_PageCapReturnsError(t *testing.T) {
	pager := &countingReservationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(pager)

	_, err := client.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Equal(t, maxReservationsPages, pager.calls)
}

func TestGetExistingCommitments_CancelledContextReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingReservationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(pager)

	_, err := client.GetExistingCommitments(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, pager.calls)
}

func TestGetExistingCommitments_CapIsExact(t *testing.T) {
	atCap := &countingReservationsPager{limit: maxReservationsPages}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(atCap)
	_, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err, "exactly cap pages must succeed")
	assert.Equal(t, maxReservationsPages, atCap.calls)

	overCap := &countingReservationsPager{limit: maxReservationsPages + 1}
	client = NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(overCap)
	_, err = client.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Equal(t, maxReservationsPages, overCap.calls)
}

// cancelOnSecondCallPager cancels the context while serving its second page.
type cancelOnSecondCallPager struct {
	calls  int
	cancel context.CancelFunc
}

func (p *cancelOnSecondCallPager) More() bool { return true }
func (p *cancelOnSecondCallPager) NextPage(_ context.Context) (armconsumption.ReservationsDetailsClientListResponse, error) {
	p.calls++
	if p.calls == 2 {
		p.cancel()
	}
	return armconsumption.ReservationsDetailsClientListResponse{}, nil
}

func TestGetExistingCommitments_CancelMidWalkReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pager := &cancelOnSecondCallPager{cancel: cancel}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(pager)

	_, err := client.GetExistingCommitments(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, pager.calls)
}
