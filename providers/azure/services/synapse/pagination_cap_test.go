package synapse

import (
	"context"
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
