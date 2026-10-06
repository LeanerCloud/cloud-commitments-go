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
