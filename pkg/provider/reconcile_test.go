package provider

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

type fakeReconciler struct{}

func (fakeReconciler) ReconcilePurchase(context.Context, common.ReconcileRequest) common.ReconcileResult {
	return common.ReconcileResult{Verdict: common.ReconcileUnknown, Reason: "no-candidate"}
}

var _ PurchaseReconciler = fakeReconciler{}

func TestPurchaseReconcilerAssertion(t *testing.T) {
	var sc any = fakeReconciler{}
	rc, ok := sc.(PurchaseReconciler)
	if !ok {
		t.Fatal("fake does not satisfy PurchaseReconciler")
	}
	if rc.ReconcilePurchase(context.Background(), common.ReconcileRequest{}).IsFound() {
		t.Fatal("unexpected Found")
	}
}
