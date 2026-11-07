package synapse

import (
	"context"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
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

func TestGetRecommendations_PageCapReturnsError(t *testing.T) {
	t.Parallel()
	pager := &countingRecommendationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRecommendationsPager(pager)

	_, err := client.GetRecommendations(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Equal(t, maxRecsPages, pager.calls)
}

func TestGetRecommendations_CancelledContextReturnsContextError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingRecommendationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRecommendationsPager(pager)

	_, err := client.GetRecommendations(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, pager.calls)
}

// finalPartialRecommendationsPager serves exactly pages pages: full pages of
// two recommendations, then a last page of one with no next link.
type finalPartialRecommendationsPager struct {
	calls, pages int
}

func (p *finalPartialRecommendationsPager) More() bool { return p.calls < p.pages }
func (p *finalPartialRecommendationsPager) NextPage(_ context.Context) (armconsumption.ReservationRecommendationsClientListResponse, error) {
	p.calls++
	n := 2
	if p.calls == p.pages {
		n = 1
	}
	var recs []armconsumption.ReservationRecommendationClassification
	for i := 0; i < n; i++ {
		recs = append(recs, mocks.BuildLegacyReservationRecommendation(
			mocks.WithRegion("eastus"),
			mocks.WithNormalizedSize("DW1000c"),
			mocks.WithQuantity(1),
			mocks.WithCosts(5000.0, 3500.0, 1500.0),
		))
	}
	return armconsumption.ReservationRecommendationsClientListResponse{
		ReservationRecommendationsListResult: armconsumption.ReservationRecommendationsListResult{Value: recs},
	}, nil
}

func TestGetRecommendations_ExactlyCapPagesWithPartialLastPageSucceeds(t *testing.T) {
	t.Parallel()
	pager := &finalPartialRecommendationsPager{pages: maxRecsPages}
	client := newPricedTestClient()
	client.SetRecommendationsPager(pager)

	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	// Each recommendation expands to an upfront and a monthly variant.
	assert.Len(t, recs, 2*(2*(maxRecsPages-1)+1))
	assert.Equal(t, maxRecsPages, pager.calls)
}

func TestGetExistingCommitments_PageCapReturnsError(t *testing.T) {
	t.Parallel()
	pager := &reservationstest.CountingPager{Limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetInventoryFactories(factoriesFor(func() reservations.OrderReservationsPager { return pager }))

	_, err := client.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Equal(t, maxReservationsPages, pager.Calls)
}

func TestGetExistingCommitments_CancelledContextReturnsContextError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &reservationstest.CountingPager{Limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetInventoryFactories(factoriesFor(func() reservations.OrderReservationsPager { return pager }))

	_, err := client.GetExistingCommitments(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, pager.Calls)
}

func TestGetExistingCommitments_CapIsExact(t *testing.T) {
	t.Parallel()
	atCap := &reservationstest.CountingPager{Limit: maxReservationsPages}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetInventoryFactories(factoriesFor(func() reservations.OrderReservationsPager { return atCap }))
	_, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err, "exactly cap pages must succeed")
	assert.Equal(t, maxReservationsPages, atCap.Calls)

	overCap := &reservationstest.CountingPager{Limit: maxReservationsPages + 1}
	client = NewClient(nil, "test-subscription", "eastus")
	client.SetInventoryFactories(factoriesFor(func() reservations.OrderReservationsPager { return overCap }))
	_, err = client.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Equal(t, maxReservationsPages, overCap.Calls)
}

func TestGetExistingCommitments_CancelMidWalkReturnsContextError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pager := &reservationstest.CancelOnSecondCallPager{Cancel: cancel}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetInventoryFactories(factoriesFor(func() reservations.OrderReservationsPager { return pager }))

	_, err := client.GetExistingCommitments(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, pager.Calls)
}

// finalPartialReservationsPager serves exactly pages pages: full pages of two
// reservations, then a last page of one.
type finalPartialReservationsPager struct {
	calls, pages int
}

func (p *finalPartialReservationsPager) More() bool { return p.calls < p.pages }
func (p *finalPartialReservationsPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	n := 2
	if p.calls == p.pages-1 {
		n = 1
	}
	var rows []*armreservations.ReservationResponse
	for i := 0; i < n; i++ {
		rows = append(rows, synapseRow(fmt.Sprintf("res-%d-%d", p.calls, i), "DW1000c", "eastus", 1))
	}
	p.calls++
	return armreservations.ReservationClientListResponse{ReservationList: armreservations.ReservationList{Value: rows}}, nil
}

func TestGetExistingCommitments_ExactlyCapPagesWithPartialLastPageSucceeds(t *testing.T) {
	t.Parallel()
	pager := &finalPartialReservationsPager{pages: maxReservationsPages}
	client := newTestClient()
	client.SetInventoryFactories(factoriesFor(func() reservations.OrderReservationsPager { return pager }))

	got, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 2*(maxReservationsPages-1)+1)
	assert.Equal(t, maxReservationsPages, pager.calls)
}
