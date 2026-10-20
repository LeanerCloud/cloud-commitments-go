package recommendations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fastLimiter(maxRetries int) *RateLimiter {
	return NewRateLimiterWithOptions(time.Millisecond, time.Millisecond, maxRetries)
}

func TestFetchWithRetry_RetriesThrottleThenSucceeds(t *testing.T) {
	calls := 0
	got, err := fetchWithRetry(context.Background(), fastLimiter(3), "boom", func() (*string, error) {
		calls++
		if calls < 3 {
			return nil, newThrottleError()
		}
		v := "ok"
		return &v, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", *got)
	assert.Equal(t, 3, calls)
}

func TestFetchWithRetry_WrapsTerminalError(t *testing.T) {
	sentinel := errors.New("access denied")
	calls := 0
	got, err := fetchWithRetry(context.Background(), fastLimiter(3), "failed to get x", func() (*string, error) {
		calls++
		return nil, sentinel
	})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, sentinel)
	assert.Contains(t, err.Error(), "failed to get x")
	assert.Equal(t, 1, calls, "non-throttle errors must not be retried")
}

func TestFetchWithRetry_StopsAfterMaxRetries(t *testing.T) {
	calls := 0
	_, err := fetchWithRetry(context.Background(), fastLimiter(2), "p", func() (*string, error) {
		calls++
		return nil, newThrottleError()
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls)
}

func TestFetchWithRetry_CanceledContextFailsBeforeCalling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := fetchWithRetry(ctx, fastLimiter(1), "p", func() (*string, error) {
		calls++
		return nil, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls)
}
