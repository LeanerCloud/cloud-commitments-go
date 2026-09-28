// Package reservationstate classifies AWS reservation lifecycle states.
package reservationstate

import "github.com/LeanerCloud/cloud-commitments-go/pkg/common"

const paymentFailed common.CommitmentState = "payment-failed"

// IsOwned retains unknown states to prevent duplicate purchases. AWS reservation
// APIs use payment-failed rather than the provider-neutral failed state.
func IsOwned(state common.CommitmentState) bool {
	switch state {
	case common.CommitmentStateRetired, common.CommitmentStatePendingReturn,
		common.CommitmentStateExpired, common.CommitmentStateCanceled,
		common.CommitmentStateFailed, paymentFailed:
		return false
	default:
		return true
	}
}
