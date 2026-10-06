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
