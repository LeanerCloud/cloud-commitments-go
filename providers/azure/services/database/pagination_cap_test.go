package database

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/stretchr/testify/assert"
)

// countingMIPager and countingServersPager report More() until limit pages
// were requested, so a missing cap shows up as calls == limit instead of a
// hung test.
type countingMIPager struct {
	calls, limit int
}

func (p *countingMIPager) More() bool { return p.calls < p.limit }
func (p *countingMIPager) NextPage(_ context.Context) (armsql.ManagedInstancesClientListResponse, error) {
	p.calls++
	return armsql.ManagedInstancesClientListResponse{}, nil
}

type countingServersPager struct {
	calls, limit int
}

func (p *countingServersPager) More() bool { return p.calls < p.limit }
func (p *countingServersPager) NextPage(_ context.Context) (armsql.ServersClientListResponse, error) {
	p.calls++
	return armsql.ServersClientListResponse{}, nil
}

func TestWalkManagedInstances_PageCapMarksWalkIncomplete(t *testing.T) {
	pager := &countingMIPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetManagedInstancesPager(pager)

	_, _, _, complete := client.walkManagedInstances(context.Background())
	assert.False(t, complete)
	assert.Equal(t, maxSQLListPages, pager.calls)
}

func TestWalkManagedInstances_CancelledContextMarksWalkIncomplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingMIPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetManagedInstancesPager(pager)

	_, _, _, complete := client.walkManagedInstances(ctx)
	assert.False(t, complete)
	assert.Zero(t, pager.calls)
}

func TestHasRegularServers_PageCapStopsEndlessPager(t *testing.T) {
	pager := &countingServersPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetServersPager(pager)

	found, complete := client.hasRegularServers(context.Background())
	assert.False(t, found)
	assert.False(t, complete)
	assert.Equal(t, maxSQLListPages, pager.calls)
}

func TestHasRegularServers_CancelledContextStopsWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingServersPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetServersPager(pager)

	found, complete := client.hasRegularServers(ctx)
	assert.False(t, found)
	assert.False(t, complete)
	assert.Zero(t, pager.calls)
}

func TestFetchServerInfo_ServersPageCapLeavesSignalsEmpty(t *testing.T) {
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetManagedInstancesPager(&MockSQLManagedInstancesPager{pages: []armsql.ManagedInstancesClientListResponse{buildMIPage(true)}})
	client.SetServersPager(&countingServersPager{limit: 1000})

	azConfig, deployment := client.fetchServerInfo(context.Background())
	assert.Empty(t, azConfig)
	assert.Empty(t, deployment, "an incomplete server walk must not read as no servers")
}

// cancelAfterPagesMIPager cancels the context once its last page is served,
// so the managed-instance walk completes and the cancel lands on the server walk.
type cancelAfterPagesMIPager struct {
	MockSQLManagedInstancesPager
	cancel context.CancelFunc
}

func (p *cancelAfterPagesMIPager) NextPage(ctx context.Context) (armsql.ManagedInstancesClientListResponse, error) {
	page, err := p.MockSQLManagedInstancesPager.NextPage(ctx)
	p.cancel()
	return page, err
}

func TestFetchServerInfo_CancelAfterManagedWalkLeavesSignalsEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetManagedInstancesPager(&cancelAfterPagesMIPager{
		MockSQLManagedInstancesPager: MockSQLManagedInstancesPager{pages: []armsql.ManagedInstancesClientListResponse{buildMIPage(true)}},
		cancel:                       cancel,
	})
	client.SetServersPager(&MockSQLServersPager{pages: []armsql.ServersClientListResponse{{}}})

	azConfig, deployment := client.fetchServerInfo(ctx)
	assert.Empty(t, azConfig)
	assert.Empty(t, deployment)
}
