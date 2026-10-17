package cosmosdb

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v2"
	"github.com/stretchr/testify/assert"
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
	pager := &countingAccountsPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetCosmosAccountsPager(pager)

	assert.Equal(t, "", client.fetchDominantAPIType(context.Background()))
	assert.Equal(t, maxAccountsPages, pager.calls)
}

func TestFetchDominantAPIType_CancelledContextStopsWalk(t *testing.T) {
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
	pager := &finalPartialAccountsPager{pages: maxAccountsPages}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetCosmosAccountsPager(pager)

	assert.Equal(t, "mongodb", client.fetchDominantAPIType(context.Background()))
	assert.Equal(t, maxAccountsPages, pager.calls)
}
