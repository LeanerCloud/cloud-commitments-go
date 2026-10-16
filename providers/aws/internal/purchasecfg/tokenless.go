package purchasecfg

import (
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// These apply only without an idempotency token (go#299; precedent: ec2).

// SingleAttemptIf returns the per-operation option that disables SDK retries
// when token is empty, and no option otherwise.
func SingleAttemptIf[O any](token string, set func(*O)) []func(*O) {
	if token != "" {
		return nil
	}
	return []func(*O){set}
}

// ClassifyUnlessToken returns err unchanged when token is set, else ClassifyPurchaseError(err).
func ClassifyUnlessToken(token string, err error) error {
	if token != "" {
		return err
	}
	return ClassifyPurchaseError(err)
}

// UnknownUnlessToken wraps err with common.ErrOutcomeUnknown when token is empty.
func UnknownUnlessToken(token string, err error) error {
	if token != "" {
		return err
	}
	return fmt.Errorf("%w: %w", common.ErrOutcomeUnknown, err)
}
