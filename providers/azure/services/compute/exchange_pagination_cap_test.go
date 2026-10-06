package compute_test

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingExchangeablePager reports More() until limit pages were requested,
// so a missing cap shows up as calls == limit instead of a hung test.
type countingExchangeablePager struct {
	calls, limit int
}

func (p *countingExchangeablePager) More() bool { return p.calls < p.limit }
func (p *countingExchangeablePager) NextPage(_ context.Context) (armreservations.ReservationClientListAllResponse, error) {
	p.calls++
	return armreservations.ReservationClientListAllResponse{}, nil
}

func TestListExchangeableReservations_PageCapReturnsError(t *testing.T) {
	const limit = 1000
	pager := &countingExchangeablePager{limit: limit}
	client := newClient()
	client.SetExchangeablePager(pager)

	_, err := client.ListExchangeableReservations(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Less(t, pager.calls, limit)
}

func TestListExchangeableReservations_CancelledContextReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingExchangeablePager{limit: 1000}
	client := newClient()
	client.SetExchangeablePager(pager)

	_, err := client.ListExchangeableReservations(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, pager.calls)
}

// exchangeCap mirrors compute.maxReservationsPages (unexported).
const exchangeCap = 50

func TestListExchangeableReservations_CapIsExact(t *testing.T) {
	atCap := &countingExchangeablePager{limit: exchangeCap}
	client := newClient()
	client.SetExchangeablePager(atCap)
	_, err := client.ListExchangeableReservations(context.Background())
	require.NoError(t, err, "exactly cap pages must succeed")
	assert.Equal(t, exchangeCap, atCap.calls)

	overCap := &countingExchangeablePager{limit: exchangeCap + 1}
	client = newClient()
	client.SetExchangeablePager(overCap)
	_, err = client.ListExchangeableReservations(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Equal(t, exchangeCap, overCap.calls)
}

// cancelOnSecondCallPager cancels the context while serving its second page.
type cancelOnSecondCallPager struct {
	calls  int
	cancel context.CancelFunc
}

func (p *cancelOnSecondCallPager) More() bool { return true }
func (p *cancelOnSecondCallPager) NextPage(_ context.Context) (armreservations.ReservationClientListAllResponse, error) {
	p.calls++
	if p.calls == 2 {
		p.cancel()
	}
	return armreservations.ReservationClientListAllResponse{}, nil
}

func TestListExchangeableReservations_CancelMidWalkReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pager := &cancelOnSecondCallPager{cancel: cancel}
	client := newClient()
	client.SetExchangeablePager(pager)

	_, err := client.ListExchangeableReservations(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, pager.calls)
}
