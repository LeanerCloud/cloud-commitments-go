package skulist

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

type countingService struct {
	resp  *cloudbilling.ListSkusResponse
	err   error
	ids   []string
	calls atomic.Int32
	mu    sync.Mutex
}

func (s *countingService) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.ids = append(s.ids, serviceID)
	s.mu.Unlock()
	return s.resp, s.err
}

func TestCache_ListsOnceAndPassesServiceID(t *testing.T) {
	t.Parallel()
	want := &cloudbilling.ListSkusResponse{Skus: []*cloudbilling.Sku{{SkuId: "a"}}}
	svc := &countingService{resp: want}
	var c Cache
	for i := 0; i < 3; i++ {
		got, err := c.Get(svc, "services/x")
		require.NoError(t, err)
		assert.Same(t, want, got)
	}
	assert.EqualValues(t, 1, svc.calls.Load())
	assert.Equal(t, []string{"services/x"}, svc.ids)
}

func TestCache_ErrorIsNotCached(t *testing.T) {
	t.Parallel()
	svc := &countingService{err: errors.New("boom")}
	var c Cache
	_, err := c.Get(svc, "services/x")
	require.EqualError(t, err, "boom")

	svc.err, svc.resp = nil, &cloudbilling.ListSkusResponse{}
	got, err := c.Get(svc, "services/x")
	require.NoError(t, err)
	assert.Same(t, svc.resp, got)
	assert.EqualValues(t, 2, svc.calls.Load())
}

func TestCache_ConcurrentGetsShareOneListing(t *testing.T) {
	t.Parallel()
	svc := &countingService{resp: &cloudbilling.ListSkusResponse{}}
	var c Cache
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.Get(svc, "services/x")
			assert.NoError(t, err)
			assert.Same(t, svc.resp, got)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, svc.calls.Load())
}
