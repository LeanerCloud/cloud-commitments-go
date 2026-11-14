package common

import (
	"testing"
	"time"
)

func TestMemoryPurchaseClaimer_Conformance(t *testing.T) {
	t.Parallel()
	RunPurchaseClaimerConformance(t, func(*testing.T) (PurchaseClaimer, func(time.Duration)) {
		now := time.Unix(1_000_000, 0)
		return NewMemoryPurchaseClaimer(func() time.Time { return now }), func(d time.Duration) { now = now.Add(d) }
	})
}
