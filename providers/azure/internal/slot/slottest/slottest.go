// Package slottest lets a package's tests prove that a real client method
// holds the shared concurrency slot (internal/slot) during each outbound call.
package slottest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
)

// Probe owns a cap-1 shared semaphore. A fake pager or HTTP client calls
// Check from inside its call: with the slot held, TryAcquire must fail.
type Probe struct {
	t     *testing.T
	sem   *semaphore.Weighted
	calls atomic.Int32
}

// New returns a Probe and a context carrying its semaphore, bounded by a
// 30-second deadline so a stuck test cannot hang the suite.
func New(t *testing.T) (*Probe, context.Context) {
	t.Helper()
	sem := semaphore.NewWeighted(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return &Probe{t: t, sem: sem}, concurrency.WithSharedSemaphore(ctx, sem)
}

// Check records one call and fails the test if the slot is not held now.
func (p *Probe) Check() {
	p.calls.Add(1)
	if p.sem.TryAcquire(1) {
		p.sem.Release(1)
		p.t.Error("outbound call ran without holding the shared concurrency slot")
	}
}

// RequireCalled fails unless at least one wrapped call reached Check, so a
// test cannot pass by never exercising the site.
func (p *Probe) RequireCalled() {
	p.t.Helper()
	if p.calls.Load() == 0 {
		p.t.Fatal("no outbound call reached the fake: the site under test was not exercised")
	}
}

// Free fails if the slot is still held, i.e. a call leaked it.
func (p *Probe) Free() {
	p.t.Helper()
	if !p.sem.TryAcquire(1) {
		p.t.Fatal("shared concurrency slot still held after the call returned")
	}
	p.sem.Release(1)
}
