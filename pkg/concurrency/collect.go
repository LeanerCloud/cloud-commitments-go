package concurrency

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// Unlimited removes the concurrency cap of CollectConcurrently (errgroup
// SetLimit(-1)).
const Unlimited = -1

// Outcome is one task's result, index-aligned with the tasks slice.
type Outcome[T any] struct {
	Err   error
	Value T
}

// CollectConcurrently runs each task on its own goroutine, at most limit at a
// time. Task errors never cancel siblings: each lands in its Outcome. It
// panics on limit == 0 (errgroup would block forever). It returns ctx.Err()
// after every task has finished, so a canceled parent is never reported as
// success. Tasks receive ctx itself, so a caller that keeps using ctx after the
// helper returns sees it live.
//
// The helper never acquires the shared semaphore. Leaf calls take their own
// permit, because holding one across a whole sweep deadlocks at cap 1.
func CollectConcurrently[T any](ctx context.Context, limit int, tasks []func(context.Context) (T, error)) ([]Outcome[T], error) {
	if limit == 0 {
		panic("concurrency: CollectConcurrently limit must not be 0")
	}
	out := make([]Outcome[T], len(tasks))
	var g errgroup.Group
	g.SetLimit(limit)
	for i, task := range tasks {
		g.Go(func() error {
			out[i].Value, out[i].Err = task(ctx)
			return nil // error isolation: never propagate to errgroup
		})
	}
	_ = g.Wait() //nolint:errcheck // always nil: every task error is isolated into its Outcome
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
