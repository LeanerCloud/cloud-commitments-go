package synapse

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The counting pagers report More() until limit pages were requested, so a
// missing cap shows up as calls == limit instead of a hung test.
type countingRecommendationsPager struct {
	calls, limit int
}

func (p *countingRecommendationsPager) More() bool { return p.calls < p.limit }
func (p *countingRecommendationsPager) NextPage(_ context.Context) (armconsumption.ReservationRecommendationsClientListResponse, error) {
	p.calls++
	return armconsumption.ReservationRecommendationsClientListResponse{}, nil
}

type countingReservationsPager struct {
	calls, limit int
}

func (p *countingReservationsPager) More() bool { return p.calls < p.limit }
func (p *countingReservationsPager) NextPage(_ context.Context) (armconsumption.ReservationsDetailsClientListResponse, error) {
	p.calls++
	return armconsumption.ReservationsDetailsClientListResponse{}, nil
}

func TestGetRecommendations_PageCapReturnsError(t *testing.T) {
	pager := &countingRecommendationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRecommendationsPager(pager)

	_, err := client.GetRecommendations(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Equal(t, maxRecsPages, pager.calls)
}

func TestGetRecommendations_CancelledContextReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingRecommendationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRecommendationsPager(pager)

	_, err := client.GetRecommendations(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, pager.calls)
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
