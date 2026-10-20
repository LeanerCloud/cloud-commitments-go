package synapse

import (
	"context"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
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

func TestGetExistingCommitments_PageCapReturnsError(t *testing.T) {
	t.Parallel()
	pager := &countingReservationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(pager)

	_, err := client.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Equal(t, maxReservationsPages, pager.calls)
}

func TestGetExistingCommitments_CancelledContextReturnsContextError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingReservationsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(pager)

	_, err := client.GetExistingCommitments(ctx)
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

// finalPartialReservationsPager serves exactly pages pages: full pages of two
// reservations, then a last page of one with no next link.
type finalPartialReservationsPager struct {
	calls, pages int
}

func (p *finalPartialReservationsPager) More() bool { return p.calls < p.pages }
func (p *finalPartialReservationsPager) NextPage(_ context.Context) (armconsumption.ReservationsDetailsClientListResponse, error) {
	n := 2
	if p.calls == p.pages-1 {
		n = 1
	}
	var details []*armconsumption.ReservationDetail
	for i := 0; i < n; i++ {
		sku, id := "DW1000c", fmt.Sprintf("res-%d-%d", p.calls, i)
		details = append(details, &armconsumption.ReservationDetail{
			Properties: &armconsumption.ReservationDetailProperties{SKUName: &sku, ReservationID: &id},
		})
	}
	p.calls++
	return armconsumption.ReservationsDetailsClientListResponse{
		ReservationDetailsListResult: armconsumption.ReservationDetailsListResult{Value: details},
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

func TestGetExistingCommitments_ExactlyCapPagesWithPartialLastPageSucceeds(t *testing.T) {
	t.Parallel()
	pager := &finalPartialReservationsPager{pages: maxReservationsPages}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetReservationsPager(pager)

	got, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 2*(maxReservationsPages-1)+1)
	assert.Equal(t, maxReservationsPages, pager.calls)
}
