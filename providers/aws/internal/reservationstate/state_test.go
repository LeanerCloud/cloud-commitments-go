package reservationstate

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestIsOwned(t *testing.T) {
	for _, state := range []common.CommitmentState{
		common.CommitmentStateActive, common.CommitmentStatePaymentPending,
		common.CommitmentStateQueued, "exchanging", "future-provider-state", "",
	} {
		t.Run(string(state), func(t *testing.T) { assert.True(t, IsOwned(state)) })
	}
	for _, state := range []common.CommitmentState{
		common.CommitmentStateRetired, common.CommitmentStatePendingReturn,
		common.CommitmentStateExpired, common.CommitmentStateCanceled,
		common.CommitmentStateFailed, "payment-failed",
	} {
		t.Run(string(state), func(t *testing.T) { assert.False(t, IsOwned(state)) })
	}
}
