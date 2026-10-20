package recommendations

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
)

// fetchWithRetry runs one Cost Explorer call under the shared rate limiter
// and the global concurrency cap, retrying while the limiter classifies the
// error as retryable. The semaphore is held only across the SDK call, so
// back-off waits never occupy a slot. A terminal SDK error is wrapped with
// errPrefix; limiter and semaphore failures carry their own messages.
func fetchWithRetry[T any](ctx context.Context, limiter *RateLimiter, errPrefix string, call func() (*T, error)) (*T, error) {
	op := limiter.newOperation()
	for {
		if waitErr := op.Wait(ctx); waitErr != nil {
			return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
		}
		if acqErr := concurrency.Acquire(ctx); acqErr != nil {
			return nil, fmt.Errorf("concurrency acquire failed: %w", acqErr)
		}
		result, err := call()
		concurrency.Release(ctx)
		if !op.ShouldRetry(err) {
			if err != nil {
				return nil, fmt.Errorf("%s: %w", errPrefix, err)
			}
			return result, nil
		}
	}
}
