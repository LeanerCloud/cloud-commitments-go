package cosmosdb

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

// countingAccountsPager reports More() until limit pages were requested, so a
// missing cap shows up as calls == limit instead of a hung test.
type countingAccountsPager struct {
	calls, limit int
}

func (p *countingAccountsPager) More() bool { return p.calls < p.limit }
func (p *countingAccountsPager) NextPage(_ context.Context) (armcosmos.DatabaseAccountsClientListResponse, error) {
	p.calls++
	return armcosmos.DatabaseAccountsClientListResponse{}, nil
}

func TestFetchDominantAPIType_PageCapStopsEndlessPager(t *testing.T) {
	t.Parallel()
	pager := &countingAccountsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetCosmosAccountsPager(pager)

	assert.Equal(t, "", client.fetchDominantAPIType(context.Background()))
	assert.Equal(t, maxAccountsPages, pager.calls)
}

func TestFetchDominantAPIType_CancelledContextStopsWalk(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingAccountsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetCosmosAccountsPager(pager)

	assert.Equal(t, "", client.fetchDominantAPIType(ctx))
	assert.Zero(t, pager.calls)
}

// finalPartialAccountsPager serves exactly pages pages and only the last,
// partial one carries an account, so the result proves the final page was read.
type finalPartialAccountsPager struct {
	calls, pages int
}

func (p *finalPartialAccountsPager) More() bool { return p.calls < p.pages }
func (p *finalPartialAccountsPager) NextPage(_ context.Context) (armcosmos.DatabaseAccountsClientListResponse, error) {
	p.calls++
	if p.calls < p.pages {
		return armcosmos.DatabaseAccountsClientListResponse{}, nil
	}
	kind := armcosmos.DatabaseAccountKindMongoDB
	return armcosmos.DatabaseAccountsClientListResponse{
		DatabaseAccountsListResult: armcosmos.DatabaseAccountsListResult{
			Value: []*armcosmos.DatabaseAccountGetResults{{Kind: &kind}},
		},
	}, nil
}

func TestFetchDominantAPIType_ExactlyCapPagesWithPartialLastPageSucceeds(t *testing.T) {
	t.Parallel()
	pager := &finalPartialAccountsPager{pages: maxAccountsPages}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetCosmosAccountsPager(pager)

	assert.Equal(t, "mongodb", client.fetchDominantAPIType(context.Background()))
	assert.Equal(t, maxAccountsPages, pager.calls)
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
