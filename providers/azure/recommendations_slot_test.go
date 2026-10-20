package azure

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot"
)

const slotTestWait = 10 * time.Second

// slotProbe stands in for the Azure SDK pager: every NextPage call records how
// many calls are in flight, so a cap of 1 must never show a peak above 1.
type slotProbe struct {
	inflight, peak atomic.Int32
	pages          atomic.Int32
}

func (p *slotProbe) NextPage(context.Context) (int, error) {
	cur := p.inflight.Add(1)
	for {
		old := p.peak.Load()
		if cur <= old || p.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond) // widen the overlap window if the cap is not enforced
	p.inflight.Add(-1)
	return int(p.pages.Add(1)), nil
}

// pagedService fetches `pages` pages through the slot helper, optionally
// running between(pageIdx) after each page, i.e. while the sweep is between
// calls and must not hold a slot.
type pagedService struct {
	probe   *slotProbe
	pages   int
	between func(pageIdx int)
}

func (s *pagedService) GetRecommendations(ctx context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	for i := 0; i < s.pages; i++ {
		if _, err := slot.NextPage[int](ctx, s.probe); err != nil {
			return nil, err
		}
		if s.between != nil {
			s.between(i)
		}
	}
	return nil, nil
}

func slotCtx(t *testing.T, capacity int64) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*slotTestWait)
	return concurrency.WithSharedSemaphore(ctx, semaphore.NewWeighted(capacity)), cancel
}

func runWithin(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not finish within %s: sweep is holding a shared slot (deadlock or starvation)", d)
	}
}

// TestGetRecommendations_SweepsInterleavePerCallAtCapOne: with one shared slot,
// the compute sweep waits between its pages until the database sweep has made
// its first call. That can only happen if the compute sweep released the slot
// after page one.
func TestGetRecommendations_SweepsInterleavePerCallAtCapOne(t *testing.T) {
	ctx, cancel := slotCtx(t, 1)
	defer cancel()

	probe := &slotProbe{}
	dbStarted := make(chan struct{})
	var once sync.Once
	var interleaved atomic.Bool

	compute := &pagedService{probe: probe, pages: 2, between: func(i int) {
		if i != 0 {
			return
		}
		select {
		case <-dbStarted:
			interleaved.Store(true)
		case <-time.After(slotTestWait):
		}
	}}
	database := &pagedService{probe: probe, pages: 2, between: func(i int) {
		if i == 0 {
			once.Do(func() { close(dbStarted) })
		}
	}}
	overrideServiceClientFns(t, compute, database, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
	adapter := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub", getAdvisorRecsFn: noopAdvisorFn}

	runWithin(t, 3*slotTestWait, func() {
		_, err := adapter.GetRecommendations(ctx, &common.RecommendationParams{})
		assert.NoError(t, err)
	})

	assert.True(t, interleaved.Load(), "database sweep never got a slot while the compute sweep was between pages")
	assert.EqualValues(t, 4, probe.pages.Load())
	assert.EqualValues(t, 1, probe.peak.Load(), "cap 1 must bound in-flight calls to 1")
}

// TestGetRecommendations_NoDeadlockAtCapOne: a full fan-out of paged sweeps
// completes with one shared slot, so no path acquires while holding a slot.
func TestGetRecommendations_NoDeadlockAtCapOne(t *testing.T) {
	ctx, cancel := slotCtx(t, 1)
	defer cancel()

	probe := &slotProbe{}
	svc := func() serviceRecsGetter { return &pagedService{probe: probe, pages: 3} }
	overrideServiceClientFns(t, svc(), svc(), svc(), svc(), svc())
	adapter := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub", getAdvisorRecsFn: noopAdvisorFn}

	runWithin(t, 3*slotTestWait, func() {
		_, err := adapter.GetRecommendations(ctx, &common.RecommendationParams{})
		require.NoError(t, err)
	})

	assert.EqualValues(t, 15, probe.pages.Load())
	assert.EqualValues(t, 1, probe.peak.Load())
}
