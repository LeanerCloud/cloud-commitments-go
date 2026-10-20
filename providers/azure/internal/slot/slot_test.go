package slot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
)

func TestDo_ReleasesSlotOnErrorAndCanceledWait(t *testing.T) {
	t.Parallel()
	sem := semaphore.NewWeighted(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = concurrency.WithSharedSemaphore(ctx, sem)

	boom := errors.New("boom")
	_, err := Do(ctx, func() (int, error) {
		assert.False(t, sem.TryAcquire(1), "slot must be held while fn runs")
		return 0, boom
	})
	require.ErrorIs(t, err, boom)
	require.True(t, sem.TryAcquire(1), "slot must be released after fn fails")

	canceled, stop := context.WithCancel(ctx)
	stop()
	called := false
	_, err = Do(canceled, func() (int, error) { called = true; return 0, nil })
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called, "fn must not run when the slot wait is canceled")
	sem.Release(1)
}

func TestDo_NoSemaphoreRunsFn(t *testing.T) {
	t.Parallel()
	v, err := Do(context.Background(), func() (int, error) { return 7, nil })
	require.NoError(t, err)
	assert.Equal(t, 7, v)
}
