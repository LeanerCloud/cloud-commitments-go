package database

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flakyMIPager fails its first failures NextPage calls, then serves one page.
// A failed call serves nothing, so More stays true and the next walk retries.
type flakyMIPager struct {
	mu       sync.Mutex
	failures int
	calls    int
	served   bool
}

func (p *flakyMIPager) More() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.served
}

func (p *flakyMIPager) NextPage(_ context.Context) (armsql.ManagedInstancesClientListResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failures > 0 {
		p.failures--
		return armsql.ManagedInstancesClientListResponse{}, errors.New("transient")
	}
	p.served = true
	return buildMIPage(true), nil
}

func newServerInfoClient(p SQLManagedInstancesPager) *Client {
	c := NewClient(nil, "test-subscription", "eastus")
	c.SetManagedInstancesPager(p)
	c.SetServersPager(&MockSQLServersPager{})
	c.serverInfoRetryDelay = time.Nanosecond
	return c
}

func TestServerInfo_TransientFailureIsNotCached(t *testing.T) {
	p := &flakyMIPager{failures: 1}
	c := newServerInfoClient(p)
	ctx := context.Background()

	assert.Empty(t, c.cachedDominantAZConfig(ctx), "failed walk reports unknown")
	time.Sleep(time.Millisecond)
	assert.Equal(t, "zoneRedundant", c.cachedDominantAZConfig(ctx), "next lookup re-walks and gets data")
	assert.Equal(t, "managed", c.cachedDominantDeployment(ctx))
	assert.Equal(t, 2, p.calls)
}

func TestServerInfo_SuccessIsCached(t *testing.T) {
	p := &flakyMIPager{}
	c := newServerInfoClient(p)
	for i := 0; i < 10; i++ {
		require.Equal(t, "zoneRedundant", c.cachedDominantAZConfig(context.Background()))
		require.Equal(t, "managed", c.cachedDominantDeployment(context.Background()))
	}
	assert.Equal(t, 1, p.calls)
}

func TestServerInfo_CompleteEmptyWalkIsCached(t *testing.T) {
	mi := &MockSQLManagedInstancesPager{}
	c := newServerInfoClient(mi)
	for i := 0; i < 3; i++ {
		assert.Empty(t, c.cachedDominantAZConfig(context.Background()))
	}
	assert.True(t, c.serverInfoLoaded, "a subscription with no servers is a real answer")
}

func TestServerInfo_FailureBackoffSuppressesRewalk(t *testing.T) {
	p := &flakyMIPager{failures: 100}
	c := newServerInfoClient(p)
	c.serverInfoRetryDelay = time.Hour
	for i := 0; i < 5; i++ {
		assert.Empty(t, c.cachedDominantAZConfig(context.Background()))
	}
	assert.Equal(t, 1, p.calls)
}

func TestServerInfo_ConcurrentCallsWalkOnce(t *testing.T) {
	p := &flakyMIPager{}
	c := newServerInfoClient(p)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Equal(t, "zoneRedundant", c.cachedDominantAZConfig(context.Background()))
			assert.Equal(t, "managed", c.cachedDominantDeployment(context.Background()))
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, p.calls)
}

func TestServerInfo_CallerCancellationDoesNotStartCooldown(t *testing.T) {
	p := &flakyMIPager{}
	c := newServerInfoClient(p)
	c.serverInfoRetryDelay = time.Hour

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Empty(t, c.cachedDominantAZConfig(canceled))
	assert.Equal(t, "zoneRedundant", c.cachedDominantAZConfig(context.Background()),
		"a live ctx must re-walk after another caller's canceled ctx")
}
