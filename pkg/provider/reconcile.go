package provider

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// PurchaseReconciler is optionally implemented by a ServiceClient whose purchase API has no idempotency
// token (AWS EC2, Redshift). Read-only. Not on ServiceClient, so no implementer or mock breaks.
// A client that does not implement it is treated by callers as Unknown.
type PurchaseReconciler interface {
	ReconcilePurchase(ctx context.Context, req common.ReconcileRequest) common.ReconcileResult
}
