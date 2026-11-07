package reservationstest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// RequireUnknownInventory asserts that c carries only what the consumption
// ReservationDetail API reports: no region, state, price, quantity or start
// date. Producers built on that API must not invent these from the client.
func RequireUnknownInventory(t *testing.T, c common.Commitment) {
	t.Helper()
	assert.Empty(t, c.Region, "region is not in ReservationDetail")
	assert.Empty(t, c.State, "state is not in ReservationDetail")
	assert.False(t, c.CostKnown)
	assert.Zero(t, c.Cost)
	assert.Zero(t, c.Count)
	assert.True(t, c.StartDate.IsZero())
}
