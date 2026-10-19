package recommendations

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
)

// semCountingCE counts coverage/utilization SDK calls so the tests below can
// assert whether a CE call escaped the shared concurrency semaphore.
type semCountingCE struct {
	mockCostExplorerAPI
	coverageCalls    atomic.Int32
	utilizationCalls atomic.Int32
	spCoverageCalls  atomic.Int32
	spUtilCalls      atomic.Int32
	onDemandCalls    atomic.Int32
}

func (m *semCountingCE) GetSavingsPlansCoverage(ctx context.Context, params *costexplorer.GetSavingsPlansCoverageInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansCoverageOutput, error) {
	m.spCoverageCalls.Add(1)
	return &costexplorer.GetSavingsPlansCoverageOutput{}, nil
}

func (m *semCountingCE) GetSavingsPlansUtilization(ctx context.Context, params *costexplorer.GetSavingsPlansUtilizationInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansUtilizationOutput, error) {
	m.spUtilCalls.Add(1)
	return &costexplorer.GetSavingsPlansUtilizationOutput{}, nil
}

func (m *semCountingCE) GetCostAndUsage(ctx context.Context, params *costexplorer.GetCostAndUsageInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	m.onDemandCalls.Add(1)
	return &costexplorer.GetCostAndUsageOutput{}, nil
}

func (m *semCountingCE) GetReservationCoverage(ctx context.Context, params *costexplorer.GetReservationCoverageInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetReservationCoverageOutput, error) {
	m.coverageCalls.Add(1)
	return &costexplorer.GetReservationCoverageOutput{}, nil
}

func (m *semCountingCE) GetReservationUtilization(ctx context.Context, params *costexplorer.GetReservationUtilizationInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetReservationUtilizationOutput, error) {
	m.utilizationCalls.Add(1)
	return &costexplorer.GetReservationUtilizationOutput{}, nil
}

// fullSemaphoreCtx returns a canceled context carrying a capacity-1 shared
// semaphore whose only slot is already held, replicating the scheduler's
// collection path when the CUDLY_MAX_PARALLELISM cap is saturated. A correct
// leaf call must block on Acquire (and surface ctx cancellation) instead of
// issuing the CE request; the pre-fix code escaped the cap and called CE
// immediately (review finding PERF-02).
func fullSemaphoreCtx(t *testing.T) context.Context {
	t.Helper()
	sem := semaphore.NewWeighted(1)
	require.NoError(t, sem.Acquire(context.Background(), 1))
	t.Cleanup(func() { sem.Release(1) })

	ctx, cancel := context.WithCancel(concurrency.WithSharedSemaphore(context.Background(), sem))
	cancel()
	return ctx
}

// TestFetchCoveragePage_RespectsSharedSemaphore is the PERF-02 regression
// test for the coverage path (scheduler's AttachDailyUsageHistory fan-out):
// with the cap saturated, fetchCoveragePage must not reach the CE SDK.
// Pre-fix this test fails: the call escaped the semaphore, hit the mock,
// and returned success.
func TestFetchCoveragePage_RespectsSharedSemaphore(t *testing.T) {
	mock := &semCountingCE{}
	client := NewClientWithAPI(mock, "us-east-1")

	_, err := client.fetchCoveragePage(fullSemaphoreCtx(t), &costexplorer.GetReservationCoverageInput{})

	require.Error(t, err, "fetch must fail when the cap is saturated and ctx is canceled")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), mock.coverageCalls.Load(),
		"GetReservationCoverage escaped the shared concurrency semaphore")
}

// TestFetchUtilizationPage_RespectsSharedSemaphore is the PERF-02 regression
// test for the utilization path: same invariant as the coverage test.
func TestFetchUtilizationPage_RespectsSharedSemaphore(t *testing.T) {
	mock := &semCountingCE{}
	client := NewClientWithAPI(mock, "us-east-1")

	_, err := client.fetchUtilizationPage(fullSemaphoreCtx(t), &costexplorer.GetReservationUtilizationInput{})

	require.Error(t, err, "fetch must fail when the cap is saturated and ctx is canceled")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), mock.utilizationCalls.Load(),
		"GetReservationUtilization escaped the shared concurrency semaphore")
}

// TestFetchPages_ReleaseSemaphoreSlot verifies the Acquire/Release pairing:
// after a successful fetch under a free capacity-1 semaphore, the slot must
// be available again (no leak that would starve later leaf calls).
func TestFetchPages_ReleaseSemaphoreSlot(t *testing.T) {
	mock := &semCountingCE{}
	client := NewClientWithAPI(mock, "us-east-1")

	sem := semaphore.NewWeighted(1)
	ctx := concurrency.WithSharedSemaphore(context.Background(), sem)

	_, err := client.fetchCoveragePage(ctx, &costexplorer.GetReservationCoverageInput{})
	require.NoError(t, err)
	_, err = client.fetchUtilizationPage(ctx, &costexplorer.GetReservationUtilizationInput{})
	require.NoError(t, err)

	assert.Equal(t, int32(1), mock.coverageCalls.Load())
	assert.Equal(t, int32(1), mock.utilizationCalls.Load())
	require.True(t, sem.TryAcquire(1), "semaphore slot leaked: fetch did not release after the SDK call")
	sem.Release(1)
}

// heldSlotCtx is like fullSemaphoreCtx but the context is live with a short
// deadline. The rate limiter wait succeeds, so only a missing Acquire lets the
// call through; a correct fetcher blocks on Acquire until the deadline.
func heldSlotCtx(t *testing.T) context.Context {
	t.Helper()
	sem := semaphore.NewWeighted(1)
	require.NoError(t, sem.Acquire(context.Background(), 1))
	t.Cleanup(func() { sem.Release(1) })
	ctx, cancel := context.WithTimeout(concurrency.WithSharedSemaphore(context.Background(), sem), 200*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// TestFetchSPAndOnDemandPages_RespectSharedSemaphore is the issue #8 (A07-018)
// regression test: with the cap saturated, the SP coverage, SP utilization and
// on-demand fetchers must block on Acquire instead of reaching the CE SDK.
func TestFetchSPAndOnDemandPages_RespectSharedSemaphore(t *testing.T) {
	cases := []struct {
		name  string
		call  func(c *Client, ctx context.Context) error
		calls func(m *semCountingCE) int32
	}{
		{"sp coverage", func(c *Client, ctx context.Context) error {
			_, err := c.fetchSPCoveragePage(ctx, &costexplorer.GetSavingsPlansCoverageInput{})
			return err
		}, func(m *semCountingCE) int32 { return m.spCoverageCalls.Load() }},
		{"sp utilization", func(c *Client, ctx context.Context) error {
			_, err := c.fetchSPUtilizationPage(ctx, &costexplorer.GetSavingsPlansUtilizationInput{})
			return err
		}, func(m *semCountingCE) int32 { return m.spUtilCalls.Load() }},
		{"on-demand", func(c *Client, ctx context.Context) error {
			_, err := c.fetchOnDemandPage(ctx, &costexplorer.GetCostAndUsageInput{})
			return err
		}, func(m *semCountingCE) int32 { return m.onDemandCalls.Load() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &semCountingCE{}
			client := NewClientWithAPI(mock, "us-east-1")

			err := tc.call(client, heldSlotCtx(t))

			require.Error(t, err)
			assert.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Equal(t, int32(0), tc.calls(mock), "CE call escaped the shared concurrency semaphore")
		})
	}
}

// TestFetchSPAndOnDemandPages_ReleaseSemaphoreSlot checks the slot is returned
// after each successful call.
func TestFetchSPAndOnDemandPages_ReleaseSemaphoreSlot(t *testing.T) {
	mock := &semCountingCE{}
	client := NewClientWithAPI(mock, "us-east-1")
	sem := semaphore.NewWeighted(1)
	// Bounded so a leaked slot fails the test instead of hanging it.
	ctx, cancel := context.WithTimeout(concurrency.WithSharedSemaphore(context.Background(), sem), 2*time.Second)
	defer cancel()

	_, err := client.fetchSPCoveragePage(ctx, &costexplorer.GetSavingsPlansCoverageInput{})
	require.NoError(t, err)
	_, err = client.fetchSPUtilizationPage(ctx, &costexplorer.GetSavingsPlansUtilizationInput{})
	require.NoError(t, err)
	_, err = client.fetchOnDemandPage(ctx, &costexplorer.GetCostAndUsageInput{})
	require.NoError(t, err)

	assert.Equal(t, int32(1), mock.spCoverageCalls.Load())
	assert.Equal(t, int32(1), mock.spUtilCalls.Load())
	assert.Equal(t, int32(1), mock.onDemandCalls.Load())
	require.True(t, sem.TryAcquire(1), "semaphore slot leaked")
	sem.Release(1)
}
