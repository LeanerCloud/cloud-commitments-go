package concurrency

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

func TestCollectConcurrently_IndexAlignedUnderReversedCompletion(t *testing.T) {
	tasks := make([]func(context.Context) (int, error), 4)
	for i := range tasks {
		tasks[i] = func(context.Context) (int, error) {
			time.Sleep(time.Duration(len(tasks)-i) * 15 * time.Millisecond)
			return i, nil
		}
	}
	out, err := CollectConcurrently(context.Background(), Unlimited, tasks)
	require.NoError(t, err)
	require.Len(t, out, 4)
	for i, o := range out {
		assert.Equal(t, i, o.Value)
		assert.NoError(t, o.Err)
	}
}

func TestCollectConcurrently_TaskErrorDoesNotCancelSiblings(t *testing.T) {
	boom := errors.New("boom")
	tasks := []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "", boom },
		func(ctx context.Context) (string, error) {
			time.Sleep(30 * time.Millisecond)
			return "ok", ctx.Err()
		},
	}
	out, err := CollectConcurrently(context.Background(), Unlimited, tasks)
	require.NoError(t, err)
	assert.ErrorIs(t, out[0].Err, boom)
	assert.Equal(t, "ok", out[1].Value)
	assert.NoError(t, out[1].Err)
}

func TestCollectConcurrently_HonoursLimit(t *testing.T) {
	var running, peak atomic.Int32
	task := func(context.Context) (struct{}, error) {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return struct{}{}, nil
	}
	tasks := []func(context.Context) (struct{}, error){task, task, task, task, task, task}
	_, err := CollectConcurrently(context.Background(), 2, tasks)
	require.NoError(t, err)
	assert.Equal(t, int32(2), peak.Load())
}

func TestCollectConcurrently_CanceledParentReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tasks := []func(context.Context) (int, error){
		func(context.Context) (int, error) { cancel(); return 1, nil },
	}
	out, err := CollectConcurrently(ctx, Unlimited, tasks)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, out)
}

func TestCollectConcurrently_NeverTakesSharedPermit(t *testing.T) {
	sem := semaphore.NewWeighted(1)
	require.NoError(t, sem.Acquire(context.Background(), 1)) // cap exhausted
	ctx, cancel := context.WithTimeout(WithSharedSemaphore(context.Background(), sem), 2*time.Second)
	defer cancel()
	tasks := []func(context.Context) (int, error){
		func(context.Context) (int, error) { return 7, nil },
	}
	out, err := CollectConcurrently(ctx, Unlimited, tasks)
	require.NoError(t, err)
	assert.Equal(t, 7, out[0].Value)
}

func TestCollectConcurrently_ZeroLimitPanics(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = CollectConcurrently(context.Background(), 0, []func(context.Context) (int, error){})
	})
}

func TestCollectConcurrently_NoTasks(t *testing.T) {
	out, err := CollectConcurrently[int](context.Background(), 1, nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}
