// Package slot holds the shared CUDLY_MAX_PARALLELISM slot (pkg/concurrency)
// for the duration of one outbound Azure call.
//
// The slot bounds in-flight IO, so it is taken per page fetch / HTTP request
// rather than per service sweep: a sweep walks many pages and lazily loads
// pricing and SKU catalogs, and holding one slot across all of that starves
// the AWS and GCP leaf calls sharing the semaphore.
//
// Invariant: fn must be a leaf. It must not call Do or NextPage, and no caller
// may hold a slot while calling into code that does; with a cap of 1 a nested
// acquire deadlocks. Retries and sleeps belong outside fn so a slot is never
// held across a backoff.
package slot

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
)

// Do runs fn while holding one shared slot. With no semaphore on ctx it just
// runs fn. A canceled wait for the slot is returned without running fn.
func Do[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	if err := concurrency.Acquire(ctx); err != nil {
		var zero T
		return zero, fmt.Errorf("concurrency acquire failed: %w", err)
	}
	defer concurrency.Release(ctx)
	return fn()
}

// Pager is the NextPage surface shared by the Azure SDK pagers and the
// per-service pager interfaces.
type Pager[T any] interface {
	NextPage(ctx context.Context) (T, error)
}

// NextPage fetches one page from p while holding one shared slot.
func NextPage[T any](ctx context.Context, p Pager[T]) (T, error) {
	return Do(ctx, func() (T, error) { return p.NextPage(ctx) })
}
